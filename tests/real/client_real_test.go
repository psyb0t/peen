//go:build real

package realtest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/require"
)

type realHarnessWebSocketMessage struct {
	Message string `json:"message"`
}

type realHarnessWebSocketMessageResult struct {
	Queued bool `json:"queued"`
}

func sendRealHarnessMessage(
	t *testing.T,
	process *runningRealHarnessPeen,
	sessionID uuid.UUID,
) uuid.UUID {
	t.Helper()

	connection := dialRealHarnessWebSocket(t, process)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	sent := writeRealHarnessMessage(t, connection, sessionID, realHarnessTaskMessage)

	return awaitRealHarnessWebSocketCompletion(t, process, connection, sent)
}

// writeRealHarnessMessage sends one message.send frame for a named session.
//
// The control plane routes on the session in the frame metadata, never on the
// connection, so a client names the session it wants on every message.
//
// It returns the sent frame's ID. The controller echoes that ID as the
// triggeredBy of the completion it broadcasts, and because every client on a
// session receives every completion, that ID is the only way a caller can tell
// its own answer from another client's.
func writeRealHarnessMessage(
	t *testing.T,
	connection *websocket.Conn,
	sessionID uuid.UUID,
	message string,
) uuid.UUID {
	t.Helper()

	event := dabluveees.NewEvent(
		realHarnessMessageSendEvent,
		realHarnessWebSocketMessage{Message: message},
	).SetMetadata(realHarnessWebSocketSessionID, sessionID.String())
	require.NoError(t, connection.WriteJSON(event))

	return event.ID
}

// openRealHarnessSession resolves the fixture workspace to its durable session.
//
// A controller starts with no sessions, so this is where every turn in this
// suite begins. Opening the same workspace again resumes the same session.
func openRealHarnessSession(
	t *testing.T,
	process *runningRealHarnessPeen,
	workspace string,
) uuid.UUID {
	t.Helper()

	payload, err := json.Marshal(api.OpenSessionRequest{Workspace: workspace})
	require.NoError(t, err)

	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		process.baseURL+realHarnessAPIPath+"/sessions/open",
		bytes.NewReader(payload),
	)
	require.NoError(t, err)
	request.Header.Set(realHarnessContentType, realHarnessJSONMediaType)
	request.Header.Set(
		realHarnessAuthorization,
		realHarnessBearerPrefix+process.apiToken,
	)

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)

	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equalf(
		t,
		http.StatusOK,
		response.StatusCode,
		"Peen output:\n%s",
		process.output.String(),
	)

	opened := api.OpenedSession{}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&opened))
	require.Equal(t, workspace, opened.Session.Workspace)

	return opened.Session.Id
}

func dialRealHarnessWebSocket(
	t *testing.T,
	process *runningRealHarnessPeen,
) *websocket.Conn {
	t.Helper()

	endpoint, err := url.Parse(process.baseURL)
	require.NoError(t, err)
	endpoint.Scheme = "ws"
	endpoint.Path = realHarnessWebSocketPath

	dialer := websocket.Dialer{
		HandshakeTimeout: realHarnessStartupTimeout,
		Subprotocols: []string{
			realHarnessWebSocketProtocol,
			realHarnessWebSocketBearer + base64.RawURLEncoding.EncodeToString(
				[]byte(process.apiToken),
			),
		},
	}
	connection, response, err := dialer.Dial(endpoint.String(), nil)
	if response != nil {
		t.Cleanup(func() { require.NoError(t, response.Body.Close()) })
	}
	require.NoErrorf(t, err, "Peen output:\n%s", process.output.String())
	require.Equal(t, realHarnessWebSocketProtocol, connection.Subprotocol())

	return connection
}

// awaitRealHarnessWebSocketCompletion reads until the completion that answers
// sent, and fails if that completion says the message was queued rather than
// run as its own turn.
func awaitRealHarnessWebSocketCompletion(
	t *testing.T,
	process *runningRealHarnessPeen,
	connection *websocket.Conn,
	sent uuid.UUID,
) uuid.UUID {
	t.Helper()

	sessionID, queued := awaitRealHarnessWebSocketResult(
		t,
		process,
		connection,
		sent,
	)
	require.False(t, queued)

	return sessionID
}

// awaitRealHarnessWebSocketResult reads frames until the completion triggered
// by sent arrives, and reports the session it names and whether the controller
// queued the message behind an active turn.
func awaitRealHarnessWebSocketResult(
	t *testing.T,
	process *runningRealHarnessPeen,
	connection *websocket.Conn,
	sent uuid.UUID,
) (uuid.UUID, bool) {
	t.Helper()

	for {
		event := readRealHarnessWebSocketEvent(t, process, connection)
		if event.TriggeredBy == nil || *event.TriggeredBy != sent {
			continue
		}

		switch string(event.Type) {
		case realHarnessMessageCompleted:
			result := realHarnessWebSocketMessageResult{}
			require.NoError(t, json.Unmarshal(event.Data, &result))

			return realHarnessEventSession(t, event), result.Queued
		case realHarnessMessageFailed:
			require.Failf(
				t,
				"WebSocket message failed",
				"event=%s Peen output:\n%s",
				event.Data,
				process.output.String(),
			)
		}
	}
}

// readRealHarnessWebSocketEvent reads one live frame under an idle deadline.
func readRealHarnessWebSocketEvent(
	t *testing.T,
	process *runningRealHarnessPeen,
	connection *websocket.Conn,
) dabluveees.Event {
	t.Helper()

	// Renewed per frame. A turn that keeps emitting live events is slow,
	// not stuck, and one absolute deadline cannot tell those apart.
	require.NoError(t, connection.SetReadDeadline(
		time.Now().Add(realHarnessIdleTimeout),
	))

	event := dabluveees.Event{}
	require.NoErrorf(
		t,
		connection.ReadJSON(&event),
		"Peen output:\n%s",
		process.output.String(),
	)

	return event
}

// realHarnessEventSession reads the session every live frame must name, so a
// client with many session tabs can route the frame it just received.
func realHarnessEventSession(
	t *testing.T,
	event dabluveees.Event,
) uuid.UUID {
	t.Helper()

	require.NotNil(t, event.Metadata)
	value, found := event.Metadata.Get(realHarnessWebSocketSessionID)
	require.True(t, found)
	text, isText := value.(string)
	require.True(t, isText)
	sessionID, err := uuid.Parse(text)
	require.NoError(t, err)

	return sessionID
}

func listRealHarnessMessages(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
) []api.Message {
	t.Helper()

	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		baseURL+realHarnessMessagesPath+"?limit=200&order=asc",
		nil,
	)
	require.NoError(t, err)
	setRealHarnessSessionHeaders(request, apiToken, sessionID)

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusOK, response.StatusCode)

	page := api.MessagePage{}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&page))
	require.False(t, page.HasMore)

	return page.Items
}

// listRealHarnessEvents pages the durable event stream to its end.
//
// A real turn emits far more events than one page holds, and a check that read
// only the first page would silently stop looking exactly where a long turn
// gets interesting.
func listRealHarnessEvents(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
) []api.TranscriptEvent {
	t.Helper()

	events := make([]api.TranscriptEvent, 0, realHarnessPageLimit)
	for offset := 0; offset < realHarnessMaxEvents; offset += realHarnessPageLimit {
		page := getRealHarnessJSON[api.TranscriptEventPage](
			t,
			fmt.Sprintf(
				"%s%s?order=asc&limit=%d&offset=%d",
				baseURL,
				realHarnessEventsPath,
				realHarnessPageLimit,
				offset,
			),
			apiToken,
			sessionID,
		)

		events = append(events, page.Events...)
		if !page.HasMore {
			return events
		}
	}

	require.Failf(t, "durable event stream did not end", "read %d events", len(events))

	return nil
}

func publishRealHarnessWakeEvent(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
) {
	t.Helper()

	delivery := api.SessionNoticeRequestDelivery("wake")
	payload, err := json.Marshal(api.SessionNoticeRequest{
		Type:     realHarnessWakeEvent,
		Summary:  "Review the completed fixture.",
		Delivery: &delivery,
	})
	require.NoError(t, err)

	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		baseURL+realHarnessNoticesPath,
		bytes.NewReader(payload),
	)
	require.NoError(t, err)
	request.Header.Set(realHarnessContentType, realHarnessJSONMediaType)
	setRealHarnessSessionHeaders(request, apiToken, sessionID)

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusAccepted, response.StatusCode)
}

func waitForRealHarnessWake(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
) {
	t.Helper()

	deadline := time.Now().Add(realHarnessTestTimeout)
	for time.Now().Before(deadline) {
		session := getRealHarnessJSON[api.Session](
			t,
			baseURL+realHarnessSessionPath,
			apiToken,
			sessionID,
		)
		if !session.ActiveTurn && session.CompletedTurnCount >= 2 {
			return
		}

		time.Sleep(realHarnessPollInterval)
	}

	require.Fail(t, "wake turn did not complete")
}

//nolint:ireturn // The type parameter is the decoded JSON value, not an interface.
func getRealHarnessJSON[T any](
	t *testing.T,
	endpoint, apiToken string,
	sessionID uuid.UUID,
) T {
	t.Helper()

	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		endpoint,
		nil,
	)
	require.NoError(t, err)
	setRealHarnessSessionHeaders(request, apiToken, sessionID)

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusOK, response.StatusCode)

	result := *new(T)
	require.NoError(t, json.NewDecoder(response.Body).Decode(&result))

	return result
}

func setRealHarnessSessionHeaders(
	request *http.Request,
	apiToken string,
	sessionID uuid.UUID,
) {
	request.Header.Set(realHarnessAuthorization, realHarnessBearerPrefix+apiToken)
	request.Header.Set(realHarnessSessionIDHeader, sessionID.String())
}
