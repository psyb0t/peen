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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testWebSocketReadTimeout  = 2 * time.Second
	testWebSocketAgentEvent   = "message.delta"
	testWebSocketAgentMessage = "hello"
)

func TestWebSocketBroadcastsSessionEventsToGlobalClients(t *testing.T) {
	sessionID := uuid.New()
	runtime := newTestRuntime(sessionID)
	runtime.runEvent = agent.Event{
		Type:    testWebSocketAgentEvent,
		Payload: json.RawMessage(`{"text":"hello"}`),
	}
	instance, err := New(Dependencies{
		Runtime:  runtime,
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

	inbound := newWebSocketMessage(
		sessionID,
		agent.MessageRequest{Message: testWebSocketAgentMessage},
	)
	require.NoError(t, first.WriteJSON(inbound))

	assertWebSocketAgentEvent(t, first, sessionID, inbound.ID, runtime.runEvent)
	assertWebSocketAgentEvent(t, second, sessionID, inbound.ID, runtime.runEvent)
}

func TestWebSocketFiltersOutboundEventsBySession(t *testing.T) {
	sessionID := uuid.New()
	otherSessionID := uuid.New()
	runtime := newTestRuntime(sessionID)
	runtime.runEvent = agent.Event{
		Type:    testWebSocketAgentEvent,
		Payload: json.RawMessage(`{"text":"hello"}`),
	}
	instance, err := New(Dependencies{Runtime: runtime, APIToken: testAPIToken})
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

	inbound := newWebSocketMessage(
		sessionID,
		agent.MessageRequest{Message: testWebSocketAgentMessage},
	)
	require.NoError(t, global.WriteJSON(inbound))

	assertWebSocketAgentEvent(t, global, sessionID, inbound.ID, runtime.runEvent)
	assertWebSocketAgentEvent(t, matching, sessionID, inbound.ID, runtime.runEvent)
	assertNoWebSocketEvent(t, nonMatching)
}

func TestWebSocketRejectsUnknownMessageFields(t *testing.T) {
	sessionID := uuid.New()
	runtime := newTestRuntime(sessionID)
	instance, err := New(Dependencies{Runtime: runtime})
	require.NoError(t, err)
	t.Cleanup(instance.webSocketHub.Close)

	httpServer := httptest.NewServer(instance.testHandler)
	t.Cleanup(httpServer.Close)

	connection := dialWebSocket(t, httpServer.URL, nil)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	inbound := newWebSocketMessage(
		sessionID,
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

func TestWebSocketRejectsMissingSessionIDPrivately(t *testing.T) {
	instance, err := New(Dependencies{Runtime: newTestRuntime(uuid.New())})
	require.NoError(t, err)
	t.Cleanup(instance.webSocketHub.Close)

	httpServer := httptest.NewServer(instance.testHandler)
	t.Cleanup(httpServer.Close)

	sender := dialWebSocket(t, httpServer.URL, nil)
	t.Cleanup(func() { require.NoError(t, sender.Close()) })
	observer := dialWebSocket(t, httpServer.URL, nil)
	t.Cleanup(func() { require.NoError(t, observer.Close()) })
	inbound := dabluveees.NewEvent(
		webSocketMessageSendEventType,
		agent.MessageRequest{Message: testWebSocketAgentMessage},
	)
	require.NoError(t, sender.WriteJSON(inbound))

	received := readWebSocketEvent(t, sender)
	assert.Equal(t, webSocketMessageFailedEventType, string(received.Type))
	assertWebSocketFailureMetadata(t, received, inbound.ID)
	assertNoWebSocketEvent(t, observer)
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
	sessionID uuid.UUID,
	data any,
) dabluveees.Event {
	return dabluveees.NewEvent(
		webSocketMessageSendEventType,
		data,
	).SetMetadata(webSocketMetadataSessionID, sessionID.String())
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

func assertWebSocketFailureMetadata(
	t *testing.T,
	event dabluveees.Event,
	triggeringEventID uuid.UUID,
) {
	t.Helper()
	require.NotNil(t, event.Metadata)
	_, hasSessionID := event.Metadata.Get(webSocketMetadataSessionID)
	assert.False(t, hasSessionID)
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
