//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/require"
)

const (
	wakeTestEventType    = "test.api.wake"
	wakeTestHandlerFile  = ".agents/events/" + wakeTestEventType + ".md"
	wakeTestHandler      = "---\ntype: " + wakeTestEventType + "\ndelivery: wake\n---\nSay that the wake reached you."
	wakeTestNoticeBody   = `{"type":"` + wakeTestEventType + `","summary":"an outside system needs the agent"}`
	wakeTestStreamWindow = 2 * time.Minute

	wakeTestJobEventType    = "job.exited"
	wakeTestJobHandlerFile  = ".agents/events/" + wakeTestJobEventType + ".md"
	wakeTestJobHandler      = "---\ntype: " + wakeTestJobEventType + "\ndelivery: wake\n---\nA background job ended. Say why it stopped."
	wakeTestJobQueueHandler = "---\ntype: " + wakeTestJobEventType + "\ndelivery: queue\n---\nA background job ended."
	wakeTestJobFixtureFile  = "job-wake-fixture.txt"
	wakeTestJobMessage      = "start the app that crashes in the background"
	wakeTestJobCommand      = "sleep 2; echo fatal >&2; exit 1"
	wakeTestJobPurpose      = "an app that crashes after its turn"
	wakeTestJobFinalAnswer  = "the app is running"
)

// A turn an event starts runs in the session's worker and streams to a client
// watching the session, the same as a turn the client sent. Nobody sends a
// message here: the notice alone has to produce the live turn.
func TestAPIWokenTurnStreamsToTheSession(t *testing.T) {
	require.NoError(t, integrationInfra.WriteWorkspaceFile(
		t.Context(),
		wakeTestHandlerFile,
		[]byte(wakeTestHandler),
	))

	sessionID := openAPIWorkspaceSession(t)
	connection := dialAPIWebSocket(t, &sessionID)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	response := apiRequest(
		t,
		http.MethodPost,
		sessionNoticesPath,
		[]byte(wakeTestNoticeBody),
		withHeader(
			withHeader(authenticatedHeaders(), headerSessionID, sessionID.String()),
			headerContentType,
			jsonMediaType,
		),
	)
	requireAPIStatus(t, response, http.StatusAccepted)

	origin := awaitWokenTurnStart(t, connection)
	require.Equal(t, wakeTestEventType, origin)

	awaitWebSocketEvent(t, connection, apiTestWebSocketTurnCompleted)
}

// A background command that ends after its turn wakes the session in its
// worker when a job.exited handler asks for it.
func TestAPIBackgroundJobExitWakesTheSession(t *testing.T) {
	require.NoError(t, integrationInfra.WriteWorkspaceFile(
		t.Context(),
		wakeTestJobHandlerFile,
		[]byte(wakeTestJobHandler),
	))
	// Later tests also run background jobs, so the handler goes back to
	// queueing once this test is done.
	t.Cleanup(func() { queueJobExits(t) })
	t.Cleanup(integrationInfra.DisableScriptedToolTurn)

	fixturePath := hostToolsContainerPath(wakeTestJobFixtureFile)
	seedContainerFile(t, fixturePath, backgroundJobFixtureContent)

	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage:       wakeTestJobMessage,
		ReadFileArguments: map[string]any{"path": fixturePath},
		EditFileArguments: map[string]any{
			"path": fixturePath,
			"edits": []map[string]any{{
				"old": backgroundJobFixtureContent,
				"new": backgroundJobFixtureEdited,
			}},
		},
		RunCommandArguments: map[string]any{
			"command":    wakeTestJobCommand,
			"purpose":    wakeTestJobPurpose,
			"background": true,
		},
		FinalAnswer: wakeTestJobFinalAnswer,
	})

	sessionID := openAPIWorkspaceSession(t)
	connection := dialAPIWebSocket(t, &sessionID)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	result := sendAPIWebSocketMessage(t, wakeTestJobMessage)
	require.Equal(t, sessionID, result.sessionID)
	integrationInfra.DisableScriptedToolTurn()

	origin := awaitWokenTurnStart(t, connection)
	require.Equal(t, wakeTestJobEventType, origin)

	awaitWebSocketEvent(t, connection, apiTestWebSocketTurnCompleted)
}

func queueJobExits(t *testing.T) {
	t.Helper()

	// Cleanup runs after the test context is cancelled, so the write gets a
	// context of its own.
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	require.NoError(t, integrationInfra.WriteWorkspaceFile(
		ctx,
		wakeTestJobHandlerFile,
		[]byte(wakeTestJobQueueHandler),
	))
}

// awaitWokenTurnStart reads until a turn.started that an event caused, and
// returns that event's type.
func awaitWokenTurnStart(t *testing.T, connection *websocket.Conn) string {
	t.Helper()

	for {
		received := readWebSocketEvent(t, connection)
		if received.Type != apiTestWebSocketTurnStarted {
			continue
		}

		started := struct {
			OriginEventType string `json:"originEventType"`
		}{}
		require.NoError(t, json.Unmarshal(received.Data, &started))

		if started.OriginEventType != "" {
			return started.OriginEventType
		}
	}
}

func awaitWebSocketEvent(
	t *testing.T,
	connection *websocket.Conn,
	eventType string,
) {
	t.Helper()

	for {
		if string(readWebSocketEvent(t, connection).Type) == eventType {
			return
		}
	}
}

func readWebSocketEvent(
	t *testing.T,
	connection *websocket.Conn,
) dabluveees.Event {
	t.Helper()

	require.NoError(t, connection.SetReadDeadline(
		time.Now().Add(wakeTestStreamWindow),
	))

	received := dabluveees.Event{}
	require.NoError(
		t,
		connection.ReadJSON(&received),
		"the woken turn never streamed to the session",
	)

	return received
}
