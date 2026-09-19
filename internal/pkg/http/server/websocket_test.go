package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/psyb0t/aichteeteapee"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testWebSocketReadTimeout  = 2 * time.Second
	testWebSocketAgentEvent   = "message.delta"
	testWebSocketAgentMessage = "hello"

	// webSocketClientPollInterval paces the wait for the server to finish
	// registering a dialed client.
	webSocketClientPollInterval = 5 * time.Millisecond
)

// A worker's events reach every global client through the durable relay.
//
// The turn itself runs in a worker, so the transport no longer sees per-event
// callbacks. The controller writes each event, then hands the same records here
// for delivery, which is what keeps a client from ever seeing an event the
// database does not already hold.
func TestWebSocketBroadcastsSessionEventsToGlobalClients(t *testing.T) {
	sessionID := uuid.New()
	instance, err := newTestServer(Dependencies{
		Runtime:  newTestRuntime(sessionID),
		APIToken: testAPIToken,
	})
	require.NoError(t, err)
	t.Cleanup(instance.webSocketHub.Close)

	httpServer := httptest.NewServer(instance.testHandler)
	t.Cleanup(httpServer.Close)

	first := dialWebSocket(t, httpServer.URL, nil)
	t.Cleanup(func() { require.NoError(t, first.Close()) })
	second := dialWebSocket(t, httpServer.URL, nil)
	t.Cleanup(func() { require.NoError(t, second.Close()) })

	waitForWebSocketClients(t, instance, sessionID, 2)

	requestID := uuid.New()
	durable := agent.Event{
		Type:      testWebSocketAgentEvent,
		Payload:   json.RawMessage(`{"text":"hello"}`),
		RequestID: requestID,
	}

	instance.BroadcastDurableSessionEvents(sessionID, []session.EventInput{{
		RequestID:   requestID,
		EventType:   testWebSocketAgentEvent,
		PayloadJSON: `{"text":"hello"}`,
	}})

	assertWebSocketAgentEvent(t, first, sessionID, uuid.Nil, durable)
	assertWebSocketAgentEvent(t, second, sessionID, uuid.Nil, durable)
}

// A client that asked for one session's feed sees only that session's durable
// events.
func TestWebSocketFiltersOutboundEventsBySession(t *testing.T) {
	sessionID := uuid.New()
	otherSessionID := uuid.New()

	instance, err := newTestServer(Dependencies{
		Runtime:  newTestRuntime(sessionID),
		APIToken: testAPIToken,
	})
	require.NoError(t, err)
	t.Cleanup(instance.webSocketHub.Close)

	httpServer := httptest.NewServer(instance.testHandler)
	t.Cleanup(httpServer.Close)

	global := dialWebSocket(t, httpServer.URL, nil)
	t.Cleanup(func() { require.NoError(t, global.Close()) })
	matching := dialWebSocket(t, httpServer.URL, &sessionID)
	t.Cleanup(func() { require.NoError(t, matching.Close()) })
	nonMatching := dialWebSocket(t, httpServer.URL, &otherSessionID)
	t.Cleanup(func() { require.NoError(t, nonMatching.Close()) })

	// The global client and the matching filter both receive this session; the
	// non-matching filter must not. Both filters have to be registered before
	// the counts mean anything: a connected client whose filter has not landed
	// yet still counts as deliverable for every session.
	waitForWebSocketSessionFilters(t, instance, 2)
	waitForWebSocketClients(t, instance, sessionID, 2)
	waitForWebSocketClients(t, instance, otherSessionID, 2)

	requestID := uuid.New()
	durable := agent.Event{
		Type:      testWebSocketAgentEvent,
		Payload:   json.RawMessage(`{"text":"hello"}`),
		RequestID: requestID,
	}

	instance.BroadcastDurableSessionEvents(sessionID, []session.EventInput{{
		RequestID:   requestID,
		EventType:   testWebSocketAgentEvent,
		PayloadJSON: `{"text":"hello"}`,
	}})

	assertWebSocketAgentEvent(t, global, sessionID, uuid.Nil, durable)
	assertWebSocketAgentEvent(t, matching, sessionID, uuid.Nil, durable)
	assertNoWebSocketEvent(t, nonMatching)
}

// A filter is recorded before the upgrade puts its client in the hub. A
// broadcast landing in that window must not discard the filter, or the client
// finishes connecting as a global subscriber and receives every session.
func TestWebSocketKeepsAFilterRecordedBeforeItsClientRegisters(t *testing.T) {
	sessionID := uuid.New()
	otherSessionID := uuid.New()
	connectingClientID := uuid.New()

	instance, err := newTestServer(Dependencies{
		Runtime:  newTestRuntime(sessionID),
		APIToken: testAPIToken,
	})
	require.NoError(t, err)
	t.Cleanup(instance.webSocketHub.Close)

	instance.setWebSocketFilter(connectingClientID, otherSessionID)

	assert.Empty(t, instance.webSocketClientsFor(sessionID))

	instance.webSocketFilterMutex.Lock()
	entry, kept := instance.webSocketFilters[connectingClientID]
	instance.webSocketFilterMutex.Unlock()

	require.True(t, kept, "the filter was dropped before its client registered")
	assert.Equal(t, otherSessionID, entry.sessionID)
	assert.False(t, entry.registered)
}

// A client message is routed to the session's worker, and the session comes
// from the transport rather than the message body.
func TestWebSocketRoutesTheMessageToTheSessionWorker(t *testing.T) {
	sessionID := uuid.New()
	runtime := newTestRuntime(sessionID)
	router := &testTurnRouter{runtime: runtime}

	instance, err := newTestServer(Dependencies{
		Runtime:  runtime,
		Turns:    router,
		APIToken: testAPIToken,
	})
	require.NoError(t, err)
	t.Cleanup(instance.webSocketHub.Close)

	httpServer := httptest.NewServer(instance.testHandler)
	t.Cleanup(httpServer.Close)

	connection := dialWebSocket(t, httpServer.URL, nil)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	inbound := newWebSocketMessage(
		agent.MessageRequest{Message: testWebSocketAgentMessage},
	)
	require.NoError(t, connection.WriteJSON(inbound))

	assertWebSocketMessageCompleted(t, connection)

	require.Len(t, router.sessions, 1)
	assert.Equal(t, sessionID, router.sessions[0])
	assert.Equal(t, testWebSocketAgentMessage, runtime.lastRequest.Message)
}

func TestWebSocketRejectsUnknownMessageFields(t *testing.T) {
	sessionID := uuid.New()
	runtime := newTestRuntime(sessionID)
	instance, err := newTestServer(Dependencies{Runtime: runtime})
	require.NoError(t, err)
	t.Cleanup(instance.webSocketHub.Close)

	httpServer := httptest.NewServer(instance.testHandler)
	t.Cleanup(httpServer.Close)

	connection := dialWebSocket(t, httpServer.URL, nil)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	inbound := newWebSocketMessage(
		json.RawMessage(`{"message":"hello","unexpected":true}`),
	)
	require.NoError(t, connection.WriteJSON(inbound))

	received := readWebSocketEvent(t, connection)
	require.Equal(t, webSocketMessageFailedEventType, string(received.Type))
	assertWebSocketMetadata(t, received, sessionID, inbound.ID)

	failure := webSocketMessageFailure{}
	require.NoError(t, json.Unmarshal(received.Data, &failure))
	assert.Equal(t, aichteeteapee.ErrorCodeValidationFailed, failure.Code)
	assert.Equal(t, webSocketMessageRejectedMessage, failure.Message)
	assert.Zero(t, runtime.runCalls)
}

// A control surface serves many workspaces, so sessionId metadata routes the
// message to one of them. The transport carries the routed session into the
// turn rather than letting the message body choose it.
func TestWebSocketRoutesMessageToTheNamedSession(t *testing.T) {
	routed := uuid.New()
	runtime := newTestRuntime(uuid.New())
	instance, err := newTestServer(Dependencies{Runtime: runtime})
	require.NoError(t, err)
	t.Cleanup(instance.webSocketHub.Close)

	httpServer := httptest.NewServer(instance.testHandler)
	t.Cleanup(httpServer.Close)

	sender := dialWebSocket(t, httpServer.URL, nil)
	t.Cleanup(func() { require.NoError(t, sender.Close()) })

	inbound := dabluveees.NewEvent(
		webSocketMessageSendEventType,
		agent.MessageRequest{Message: testWebSocketAgentMessage},
	).SetMetadata(webSocketMetadataSessionID, routed.String())
	require.NoError(t, sender.WriteJSON(inbound))

	received := readWebSocketEvent(t, sender)
	assert.Equal(t, webSocketMessageCompletedEventType, string(received.Type))

	require.NotNil(t, runtime.lastRequest.SessionID)
	assert.Equal(t, routed, *runtime.lastRequest.SessionID)
}

// A malformed sessionId never reaches the runtime, so a bad route cannot start
// a turn.
func TestWebSocketRejectsMalformedSessionIDMetadata(t *testing.T) {
	runtime := newTestRuntime(uuid.New())
	instance, err := newTestServer(Dependencies{Runtime: runtime})
	require.NoError(t, err)
	t.Cleanup(instance.webSocketHub.Close)

	httpServer := httptest.NewServer(instance.testHandler)
	t.Cleanup(httpServer.Close)

	sender := dialWebSocket(t, httpServer.URL, nil)
	t.Cleanup(func() { require.NoError(t, sender.Close()) })

	inbound := dabluveees.NewEvent(
		webSocketMessageSendEventType,
		agent.MessageRequest{Message: testWebSocketAgentMessage},
	).SetMetadata(webSocketMetadataSessionID, "not-a-uuid")
	require.NoError(t, sender.WriteJSON(inbound))

	received := readWebSocketEvent(t, sender)
	assert.Equal(t, webSocketMessageFailedEventType, string(received.Type))

	failure := webSocketMessageFailure{}
	require.NoError(t, json.Unmarshal(received.Data, &failure))
	assert.Equal(t, aichteeteapee.ErrorCodeValidationFailed, failure.Code)
	assert.Zero(t, runtime.runCalls)
}

// A control-surface runtime has no startup session, so a message that names no
// session is refused rather than run against an arbitrary workspace.
func TestWebSocketRequiresASessionWithoutAStartupSession(t *testing.T) {
	runtime := newTestRuntime(uuid.Nil)
	instance, err := newTestServer(Dependencies{Runtime: runtime})
	require.NoError(t, err)
	t.Cleanup(instance.webSocketHub.Close)

	httpServer := httptest.NewServer(instance.testHandler)
	t.Cleanup(httpServer.Close)

	sender := dialWebSocket(t, httpServer.URL, nil)
	t.Cleanup(func() { require.NoError(t, sender.Close()) })

	inbound := dabluveees.NewEvent(
		webSocketMessageSendEventType,
		agent.MessageRequest{Message: testWebSocketAgentMessage},
	)
	require.NoError(t, sender.WriteJSON(inbound))

	received := readWebSocketEvent(t, sender)
	assert.Equal(t, webSocketMessageFailedEventType, string(received.Type))
	assert.Zero(t, runtime.runCalls)
}

func TestWebSocketMessageFailureFor(t *testing.T) {
	testCases := []struct {
		name        string
		err         error
		wantCode    aichteeteapee.ErrorCode
		wantMessage string
	}{
		{
			name:        "missing session",
			err:         commerr.ErrNotFound,
			wantCode:    ErrorCodeSessionNotFound,
			wantMessage: sessionNotFoundError().Message,
		},
		{
			name:        "invalid message",
			err:         commerr.ErrValidationFailed,
			wantCode:    aichteeteapee.ErrorCodeValidationFailed,
			wantMessage: webSocketMessageRejectedMessage,
		},
		{
			name:        "cancelled turn",
			err:         commerr.ErrCancelled,
			wantCode:    ErrorCodeTurnCancelled,
			wantMessage: turnCancelledError().Message,
		},
		{
			name: "full queue",
			err: errors.Join(
				commerr.ErrConflict,
				elelem.ErrUserMessageQueueFull,
			),
			wantCode:    ErrorCodeUserMessageQueueFull,
			wantMessage: userMessageQueueFullError().Message,
		},
		{
			name:        "busy session",
			err:         commerr.ErrConflict,
			wantCode:    ErrorCodeSessionBusy,
			wantMessage: sessionBusyError("session already has an active turn").Message,
		},
		{
			name:        "unknown failure",
			err:         ctxerrors.New("unexpected"),
			wantCode:    aichteeteapee.ErrorCodeInternalServerError,
			wantMessage: webSocketMessageFailedMessage,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			code, message := webSocketMessageFailureFor(tc.err)

			assert.Equal(t, tc.wantCode, code)
			assert.Equal(t, tc.wantMessage, message)
		})
	}
}

func dialWebSocket(
	t *testing.T,
	httpURL string,
	filterSessionID *uuid.UUID,
) *websocket.Conn {
	t.Helper()

	endpoint, err := url.Parse(httpURL)
	require.NoError(t, err)
	endpoint.Scheme = "ws"
	endpoint.Path = webSocketPath
	query := endpoint.Query()
	if filterSessionID != nil {
		query.Set(webSocketSessionIDParameter, filterSessionID.String())
	}
	endpoint.RawQuery = query.Encode()

	dialer := websocket.Dialer{
		Subprotocols: []string{
			webSocketSubprotocol,
			webSocketBearerSubprotocolPrefix + base64.RawURLEncoding.EncodeToString(
				[]byte(testAPIToken),
			),
		},
	}
	connection, response, err := dialer.Dial(endpoint.String(), nil)
	if response != nil {
		t.Cleanup(func() { require.NoError(t, response.Body.Close()) })
	}
	require.NoError(t, err)
	require.Equal(t, webSocketSubprotocol, connection.Subprotocol())

	return connection
}

func newWebSocketMessage(
	data any,
) dabluveees.Event {
	return *dabluveees.NewEvent(
		webSocketMessageSendEventType,
		data,
	)
}

// waitForWebSocketClients blocks until the server has registered the expected
// number of deliverable clients for a session.
//
// Dialing returns as soon as the upgrade response is written, which is before
// the server has finished registering the connection and its session filter.
// Broadcasting in that window would drop the event and make the test flaky.
func waitForWebSocketClients(
	t *testing.T,
	instance *Server,
	sessionID uuid.UUID,
	want int,
) {
	t.Helper()

	require.Eventually(
		t,
		func() bool {
			return len(instance.webSocketClientsFor(sessionID)) == want
		},
		testWebSocketReadTimeout,
		webSocketClientPollInterval,
		"the server did not register %d clients for the session",
		want,
	)
}

// waitForWebSocketSessionFilters blocks until the server has recorded want
// session filters. Registering the connection and recording its filter are two
// steps, and an unfiltered client is deliverable for every session, so a
// broadcast in that window reaches a client that asked for a different session.
func waitForWebSocketSessionFilters(
	t *testing.T,
	instance *Server,
	want int,
) {
	t.Helper()

	require.Eventually(
		t,
		func() bool {
			instance.webSocketFilterMutex.Lock()
			defer instance.webSocketFilterMutex.Unlock()

			return len(instance.webSocketFilters) == want
		},
		testWebSocketReadTimeout,
		webSocketClientPollInterval,
		"the server did not register %d session filters",
		want,
	)
}

// assertWebSocketMessageCompleted waits for the transport's own completion
// frame, which is what the client sees once the worker answers.
func assertWebSocketMessageCompleted(
	t *testing.T,
	connection *websocket.Conn,
) {
	t.Helper()

	received := readWebSocketEvent(t, connection)
	assert.Equal(
		t,
		webSocketMessageCompletedEventType,
		string(received.Type),
	)
}

func assertWebSocketAgentEvent(
	t *testing.T,
	connection *websocket.Conn,
	sessionID uuid.UUID,
	triggeringEventID uuid.UUID,
	want agent.Event,
) {
	t.Helper()

	received := readWebSocketEvent(t, connection)
	assert.Equal(t, want.Type, string(received.Type))
	assertWebSocketMetadata(t, received, sessionID, triggeringEventID)
	assert.JSONEq(t, string(want.Payload), string(received.Data))
}

func readWebSocketEvent(
	t *testing.T,
	connection *websocket.Conn,
) dabluveees.Event {
	t.Helper()
	require.NoError(
		t,
		connection.SetReadDeadline(time.Now().Add(testWebSocketReadTimeout)),
	)

	received := dabluveees.Event{}
	require.NoError(t, connection.ReadJSON(&received))

	return received
}

func assertWebSocketMetadata(
	t *testing.T,
	event dabluveees.Event,
	sessionID uuid.UUID,
	triggeringEventID uuid.UUID,
) {
	t.Helper()
	require.NotNil(t, event.Metadata)
	assert.Equal(t, sessionID.String(), webSocketMetadataString(t, event, webSocketMetadataSessionID))
	requestID := webSocketMetadataString(t, event, webSocketMetadataRequestID)
	parsedRequestID, err := uuid.Parse(requestID)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, parsedRequestID)
	require.NotNil(t, event.TriggeredBy)
	assert.Equal(t, triggeringEventID, *event.TriggeredBy)
}

func assertNoWebSocketEvent(t *testing.T, connection *websocket.Conn) {
	t.Helper()
	require.NoError(
		t,
		connection.SetReadDeadline(time.Now().Add(testWebSocketReadTimeout)),
	)

	received := dabluveees.Event{}
	err := connection.ReadJSON(&received)
	require.Error(t, err)

	var networkErr net.Error
	ok := errors.As(err, &networkErr)
	require.True(t, ok)
	assert.True(t, networkErr.Timeout())
}

func webSocketMetadataString(
	t *testing.T,
	event dabluveees.Event,
	key string,
) string {
	t.Helper()
	value, found := event.Metadata.Get(key)
	require.True(t, found)
	stringValue, ok := value.(string)
	require.True(t, ok)

	return stringValue
}
