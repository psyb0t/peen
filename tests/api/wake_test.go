//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	"github.com/stretchr/testify/require"
)

const (
	wakeTestEventType    = "test.api.wake"
	wakeTestHandlerFile  = ".agents/events/" + wakeTestEventType + ".md"
	wakeTestHandler      = "---\ntype: " + wakeTestEventType + "\ndelivery: wake\n---\nSay that the wake reached you."
	wakeTestNoticeBody   = `{"type":"` + wakeTestEventType + `","summary":"an outside system needs the agent"}`
	wakeTestStreamWindow = 2 * time.Minute
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
