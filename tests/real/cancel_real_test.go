//go:build real

package realtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	server "github.com/psyb0t/peen/internal/pkg/http/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	realCancelPath = realHarnessSessionPath + "/cancel"

	realCancelToolResultEvent = "tool.result"

	// The task has to outlast the cancel round trip. Cancellation is checked
	// between tool rounds, so a task that finishes in two more rounds proves
	// nothing about whether cancelling works.
	realCancelTask = `This is an execution task, not a request for advice. Repeat the ` +
		`following three-step cycle ten times, and announce the cycle number before each ` +
		`one: call read_file on internal/status/status.go, then call read_file on ` +
		`internal/status/status_test.go, then call list_files on internal/status. Perform ` +
		`every cycle with its own separate tool calls. Do not batch them, do not skip a ` +
		`cycle, do not stop early, and do not modify any file.`

	// realCancelSettleTimeout bounds how long the controller may take to write
	// the cancelled turn and release the session after the worker stops.
	realCancelSettleTimeout = 2 * time.Minute
)

// TestRealHarnessCancelsALiveTurn proves a running model turn can actually be
// stopped, against a real provider rather than a held mock response.
//
// Cancellation is the one control the operator has over a turn that has gone
// wrong, and it crosses every boundary in the system: REST accepts it, the
// controller forwards it to the session worker, the worker abandons the model
// loop, and the turn is recorded as cancelled rather than failed. A mock that
// blocks inside one provider call cannot show that a turn already several tool
// rounds deep stops.
func TestRealHarnessCancelsALiveTurn(t *testing.T) {
	configured := realConfig(t)
	upstreams, err := configured.Upstreams()
	require.NoError(t, err)

	encodedUpstreams, err := json.Marshal(upstreams)
	require.NoError(t, err)

	fixture := newRealHarnessFixture(t)
	binary := buildRealHarnessBinary(t)
	process := startRealHarnessPeen(
		t,
		binary,
		fixture,
		string(encodedUpstreams),
		configured.DefaultModel,
	)
	t.Cleanup(func() { process.stop(t) })

	sessionID := openRealHarnessSession(t, process, fixture.service)
	connection := dialRealHarnessWebSocket(t, process)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	sent := writeRealHarnessMessage(t, connection, sessionID, realCancelTask)

	// Cancel on the first live tool result rather than after a REST poll for an
	// active turn. The poll costs a round trip that a fast model can spend
	// finishing the whole task, which cancels nothing and proves nothing.
	awaitRealHarnessFirstToolResult(t, process, connection)
	require.True(t, cancelRealHarnessTurn(t, process, sessionID))

	failure := awaitRealHarnessWebSocketFailure(
		t,
		process,
		connection,
		sent,
		sessionID,
	)
	assert.Equal(t, string(server.ErrorCodeTurnCancelled), failure.Code)

	waitForRealHarnessIdleSession(t, process, sessionID)
	assertRealHarnessCancelledTurn(t, process, sessionID, fixture.service)
}

type realHarnessWebSocketFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// awaitRealHarnessFirstToolResult blocks until the turn has actually run a tool.
//
// A tool result is the earliest proof that the worker is mid-loop rather than
// still assembling context, which is the state cancellation has to interrupt.
func awaitRealHarnessFirstToolResult(
	t *testing.T,
	process *runningRealHarnessPeen,
	connection *websocket.Conn,
) {
	t.Helper()

	for {
		event := readRealHarnessWebSocketEvent(t, process, connection)
		switch string(event.Type) {
		case realCancelToolResultEvent:
			return
		case realHarnessMessageCompleted, realHarnessMessageFailed:
			require.Fail(
				t,
				"the turn ended before it ran a tool",
				"event=%s Peen output:\n%s",
				event.Data,
				process.output.String(),
			)
		}
	}
}

// cancelRealHarnessTurn asks the control plane to stop the session's turn and
// reports whether it had one to stop.
func cancelRealHarnessTurn(
	t *testing.T,
	process *runningRealHarnessPeen,
	sessionID uuid.UUID,
) bool {
	t.Helper()

	request, err := http.NewRequest(
		http.MethodPost,
		process.baseURL+realCancelPath,
		nil,
	)
	require.NoError(t, err)
	setRealHarnessSessionHeaders(request, process.apiToken, sessionID)

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)

	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equalf(
		t,
		http.StatusAccepted,
		response.StatusCode,
		"Peen output:\n%s",
		process.output.String(),
	)

	result := api.CancelResponse{}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&result))

	return result.CancelRequested
}

// awaitRealHarnessWebSocketFailure reads until the failure that answers sent.
func awaitRealHarnessWebSocketFailure(
	t *testing.T,
	process *runningRealHarnessPeen,
	connection *websocket.Conn,
	sent uuid.UUID,
	sessionID uuid.UUID,
) realHarnessWebSocketFailure {
	t.Helper()

	for {
		event := readRealHarnessWebSocketEvent(t, process, connection)
		if event.TriggeredBy == nil || *event.TriggeredBy != sent {
			continue
		}

		switch event.Type {
		case realHarnessMessageFailed:
			failure := realHarnessWebSocketFailure{}
			require.NoError(t, json.Unmarshal(event.Data, &failure))
			assert.Equal(t, sessionID, realHarnessEventSession(t, event))

			return failure
		case realHarnessMessageCompleted:
			// Report what the database says about the turn the controller just
			// called complete. A turn recorded as cancelled means the durable
			// state is right and only the live event is wrong. A turn recorded
			// as completed means the cancellation never reached the model loop.
			require.Failf(
				t,
				"a cancelled turn reported completion",
				"durable turn state after cancel: %s",
				realHarnessTurnStates(t, process, sessionID),
			)
		}
	}
}

// realHarnessTurnStates summarizes the session's durable turns for a failure
// message, so a cancellation failure says what the database recorded.
func realHarnessTurnStates(
	t *testing.T,
	process *runningRealHarnessPeen,
	sessionID uuid.UUID,
) string {
	t.Helper()

	page := getRealHarnessJSON[api.TurnPage](
		t,
		process.baseURL+realHarnessTurnsPath+"?limit=200",
		process.apiToken,
		sessionID,
	)

	states := make([]string, 0, len(page.Turns))
	for _, turn := range page.Turns {
		states = append(states, fmt.Sprintf(
			"turn=%s state=%s cancelRequested=%t",
			turn.Id,
			turn.State,
			turn.CancelRequested,
		))
	}

	return strings.Join(states, "; ")
}

// waitForRealHarnessIdleSession blocks until the controller releases the turn.
func waitForRealHarnessIdleSession(
	t *testing.T,
	process *runningRealHarnessPeen,
	sessionID uuid.UUID,
) {
	t.Helper()

	deadline := time.Now().Add(realCancelSettleTimeout)
	for time.Now().Before(deadline) {
		session := getRealHarnessJSON[api.Session](
			t,
			process.baseURL+realHarnessSessionPath,
			process.apiToken,
			sessionID,
		)
		if !session.ActiveTurn {
			return
		}

		time.Sleep(realHarnessPollInterval)
	}

	require.Failf(
		t,
		"the cancelled session stayed active",
		"Peen output:\n%s",
		process.output.String(),
	)
}

// assertRealHarnessCancelledTurn checks the durable record of a cancelled turn.
//
// A cancelled turn is not a failed one. Recording it as failed would make an
// operator's own stop look like a provider or worker fault in every later read
// of this session.
func assertRealHarnessCancelledTurn(
	t *testing.T,
	process *runningRealHarnessPeen,
	sessionID uuid.UUID,
	workspace string,
) {
	t.Helper()

	page := getRealHarnessJSON[api.TurnPage](
		t,
		process.baseURL+realHarnessTurnsPath+"?limit=200",
		process.apiToken,
		sessionID,
	)
	require.Len(t, page.Turns, 1)

	turn := page.Turns[0]
	assert.Equal(t, sessionID, turn.SessionId)
	assert.Equal(t, workspace, turn.Workspace)
	assert.Equal(t, api.TurnStateCancelled, turn.State)
	assert.True(t, turn.CancelRequested)
	assert.NotNil(t, turn.CompletedAt)
}
