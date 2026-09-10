package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testWebSocketReadTimeout  = 2 * time.Second
	testWebSocketAgentEvent   = "message.delta"
	testWebSocketAgentMessage = "hello"
)

func TestWebSocketSynchronizesSessionClients(t *testing.T) {
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

	first := dialWebSocket(t, httpServer.URL, sessionID)
	t.Cleanup(func() { require.NoError(t, first.Close()) })
	second := dialWebSocket(t, httpServer.URL, sessionID)
	t.Cleanup(func() { require.NoError(t, second.Close()) })

	inbound := dabluveees.NewEvent(
		webSocketMessageSendEventType,
		api.MessageRequest{Message: testWebSocketAgentMessage},
	)
	require.NoError(t, first.WriteJSON(inbound))

	assertWebSocketAgentEvent(t, first, sessionID, runtime.runEvent)
	assertWebSocketAgentEvent(t, second, sessionID, runtime.runEvent)
}

func TestWebSocketRejectsUnknownSession(t *testing.T) {
	instance, err := New(Dependencies{
		Runtime: &testRuntime{sessionErr: commerr.ErrNotFound},
	})
	require.NoError(t, err)
	t.Cleanup(instance.webSocketHub.Close)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		webSocketPath+"?"+webSocketSessionIDParameter+"="+uuid.New().String(),
		nil,
	)
	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assertErrorCode(t, recorder, ErrorCodeSessionNotFound)
}

func dialWebSocket(
	t *testing.T,
	httpURL string,
	sessionID uuid.UUID,
) *websocket.Conn {
	t.Helper()

	endpoint, err := url.Parse(httpURL)
	require.NoError(t, err)
	endpoint.Scheme = "ws"
	endpoint.Path = webSocketPath
	query := endpoint.Query()
	query.Set(webSocketSessionIDParameter, sessionID.String())
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

func assertWebSocketAgentEvent(
	t *testing.T,
	connection *websocket.Conn,
	sessionID uuid.UUID,
	want agent.Event,
) {
	t.Helper()
	require.NoError(
		t,
		connection.SetReadDeadline(time.Now().Add(testWebSocketReadTimeout)),
	)

	received := dabluveees.Event{}
	require.NoError(t, connection.ReadJSON(&received))
	require.Equal(t, webSocketAgentEventType, string(received.Type))

	payload := webSocketAgentEvent{}
	require.NoError(t, json.Unmarshal(received.Data, &payload))
	assert.Equal(t, sessionID, payload.SessionID)
	assert.Equal(t, want.Type, payload.Event.Type)
	assert.Equal(t, want.Payload, payload.Event.Payload)
}
