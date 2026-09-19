//go:build real

package realtest

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	realSessionClientCount = 2

	realSessionQueuedMarker = "REAL_SESSION_QUEUED_FOLLOWUP_MARKER"

	realSessionTask = `This is an execution task, not a request for advice. Do not return a ` +
		`final answer until every step below has a matching tool result. In this exact order: ` +
		`(1) call list_files on the workspace root; (2) call read_file on ` +
		`internal/status/status.go; (3) call read_file on internal/status/status_test.go; ` +
		`(4) call list_files on internal/status; (5) call read_file on AGENTS.md. ` +
		`Then reply with a one-line summary. Do not modify any file.`

	realSessionFollowUp = "Additional instruction " + realSessionQueuedMarker +
		": when you summarize, state how many files you listed."

	realSessionUserMessageQueued  = "user_message.queued"
	realSessionUserMessageCreated = "user_message.created"

	// realSessionActiveTurnTimeout bounds the wait for the first turn to reach
	// the controller. It only covers session open and worker start, never the
	// model work itself.
	realSessionActiveTurnTimeout = 2 * time.Minute
)

// TestRealHarnessOneSessionServesManyClientsAndQueuesMessages proves the two
// control-plane properties a single-client test cannot reach.
//
// Many clients, one session: a second client that sent nothing still receives
// the live events and the completion of a turn another client started, each
// frame naming the session it belongs to. That is what lets a client rebuild a
// session opened on another machine.
//
// Queueing: a message sent while a turn is running does not start a second
// turn. The controller answers it with queued, appends it to the running turn's
// durable transcript, and the session still shows exactly one turn afterwards.
func TestRealHarnessOneSessionServesManyClientsAndQueuesMessages(t *testing.T) {
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

	sender := dialRealHarnessWebSocket(t, process)
	t.Cleanup(func() { require.NoError(t, sender.Close()) })

	observer := dialRealHarnessWebSocket(t, process)
	t.Cleanup(func() { require.NoError(t, observer.Close()) })

	// The observer never sends the turn. It is here to prove the controller
	// fans a session's events out to every client attached to it.
	sent := writeRealHarnessMessage(t, sender, sessionID, realSessionTask)
	collector := collectRealSessionEvents(t, process, observer, sent)

	waitForRealSessionActiveTurn(t, process, sessionID)

	queued := writeRealHarnessMessage(t, observer, sessionID, realSessionFollowUp)
	require.Equal(t, sessionID, awaitRealHarnessWebSocketCompletion(
		t,
		process,
		sender,
		sent,
	))

	observed := collector.wait(t)

	assertRealSessionFanout(t, observed, sessionID, sent)
	assertRealSessionQueuedCompletion(t, observed, sessionID, queued)
	assertRealSessionQueuedTranscript(t, process, sessionID, fixture.service)
}

// realSessionCollector reads one client's live feed on its own goroutine,
// because a websocket allows a single concurrent reader and the test still
// needs to write this client's queued message from the main goroutine.
type realSessionCollector struct {
	done   chan struct{}
	mutex  sync.Mutex
	events []dabluveees.Event
}

func collectRealSessionEvents(
	t *testing.T,
	process *runningRealHarnessPeen,
	connection *websocket.Conn,
	until uuid.UUID,
) *realSessionCollector {
	t.Helper()

	collector := &realSessionCollector{done: make(chan struct{})}

	go func() {
		defer close(collector.done)

		for {
			event := readRealHarnessWebSocketEvent(t, process, connection)

			collector.mutex.Lock()
			collector.events = append(collector.events, event)
			collector.mutex.Unlock()

			// The turn's own completion is the last frame this client can
			// expect, so it is where the feed ends rather than a timeout.
			if event.Type != realHarnessMessageCompleted {
				continue
			}

			if event.TriggeredBy != nil && *event.TriggeredBy == until {
				return
			}
		}
	}()

	return collector
}

func (c *realSessionCollector) wait(t *testing.T) []dabluveees.Event {
	t.Helper()

	select {
	case <-c.done:
	case <-time.After(realHarnessIdleTimeout):
		require.Fail(t, "observer feed did not end")
	}

	c.mutex.Lock()
	defer c.mutex.Unlock()

	return append([]dabluveees.Event(nil), c.events...)
}

// assertRealSessionFanout checks that the client which sent nothing still saw
// the other client's turn, including its live events and its completion.
func assertRealSessionFanout(
	t *testing.T,
	observed []dabluveees.Event,
	sessionID uuid.UUID,
	sent uuid.UUID,
) {
	t.Helper()

	require.NotEmpty(t, observed)

	liveEvents := 0
	completed := false

	for _, event := range observed {
		assert.Equalf(
			t,
			sessionID,
			realHarnessEventSession(t, event),
			"event %q must name its session",
			event.Type,
		)

		if event.Type == realHarnessMessageFailed {
			require.Failf(t, "observer saw a failed message", "event=%s", event.Data)
		}

		if event.Type != realHarnessMessageCompleted {
			liveEvents++

			continue
		}

		if event.TriggeredBy != nil && *event.TriggeredBy == sent {
			completed = true
		}
	}

	assert.True(t, completed, "observer missed the sender's completion")
	assert.NotZero(t, liveEvents, "observer received no live turn events")
}

// assertRealSessionQueuedCompletion checks the answer to a message sent while a
// turn was already running.
func assertRealSessionQueuedCompletion(
	t *testing.T,
	observed []dabluveees.Event,
	sessionID uuid.UUID,
	queued uuid.UUID,
) {
	t.Helper()

	for _, event := range observed {
		if event.Type != realHarnessMessageCompleted {
			continue
		}

		if event.TriggeredBy == nil || *event.TriggeredBy != queued {
			continue
		}

		result := realHarnessWebSocketMessageResult{}
		require.NoError(t, json.Unmarshal(event.Data, &result))
		assert.True(t, result.Queued, "message sent during an active turn must queue")
		assert.Equal(t, sessionID, realHarnessEventSession(t, event))

		return
	}

	require.Fail(t, "no completion answered the queued message")
}

// assertRealSessionQueuedTranscript checks what the queued message did to the
// database.
//
// The turn count is the load-bearing assertion. A queued message that opened a
// second turn would still produce a transcript entry and a durable event, and
// only the turn count shows that it joined the running turn instead.
func assertRealSessionQueuedTranscript(
	t *testing.T,
	process *runningRealHarnessPeen,
	sessionID uuid.UUID,
	workspace string,
) {
	t.Helper()

	turnIDs := assertRealHarnessDurableTurns(
		t,
		process.baseURL,
		process.apiToken,
		sessionID,
		workspace,
		1,
	)

	generationID := assertRealHarnessWorkerGeneration(
		t,
		process.baseURL,
		process.apiToken,
		sessionID,
		workspace,
	)
	events := assertRealHarnessDurableEvents(
		t,
		process.baseURL,
		process.apiToken,
		sessionID,
		turnIDs,
		generationID,
	)

	assert.True(
		t,
		realSessionHasQueuedEvent(events, realSessionUserMessageCreated),
		"the queued message was never recorded as created",
	)
	assert.True(
		t,
		realSessionHasQueuedEvent(events, realSessionUserMessageQueued),
		"the queued message produced no queued event",
	)

	messages := listRealHarnessMessages(
		t,
		process.baseURL,
		process.apiToken,
		sessionID,
	)

	delivered := false
	for _, message := range messages {
		if strings.Contains(message.Content, realSessionQueuedMarker) {
			delivered = true

			break
		}
	}

	assert.True(t, delivered, "the queued message never reached the turn transcript")
}

// realSessionHasQueuedEvent reports whether the named durable event carries the
// queued follow-up, so a match cannot come from the original message.
func realSessionHasQueuedEvent(
	events []api.TranscriptEvent,
	eventType string,
) bool {
	for _, event := range events {
		if event.Type != eventType {
			continue
		}

		message, found := event.Payload["message"].(string)
		if found && strings.Contains(message, realSessionQueuedMarker) {
			return true
		}
	}

	return false
}

// waitForRealSessionActiveTurn blocks until the controller reports a running
// turn, which is when a second message can actually be queued behind one.
func waitForRealSessionActiveTurn(
	t *testing.T,
	process *runningRealHarnessPeen,
	sessionID uuid.UUID,
) {
	t.Helper()

	deadline := time.Now().Add(realSessionActiveTurnTimeout)
	for time.Now().Before(deadline) {
		session := getRealHarnessJSON[api.Session](
			t,
			process.baseURL+realHarnessSessionPath,
			process.apiToken,
			sessionID,
		)
		if session.ActiveTurn {
			return
		}

		time.Sleep(realHarnessPollInterval)
	}

	require.Failf(
		t,
		"no turn became active",
		"Peen output:\n%s",
		process.output.String(),
	)
}
