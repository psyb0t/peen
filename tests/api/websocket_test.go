//go:build integration

package api_test

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	apiTestWebSocketPath               = apiBasePath + "/ws"
	apiTestWebSocketSessionIDParameter = "sessionId"
	apiTestWebSocketSubprotocol        = "peen.v1"
	apiTestWebSocketBearerPrefix       = "peen.bearer."
	apiTestWebSocketMessageSend        = "message.send"
	apiTestWebSocketMessageCompleted   = "message.completed"
	apiTestWebSocketMessageFailed      = "message.failed"
	apiTestWebSocketTurnStarted        = "turn.started"
	apiTestWebSocketContentBlockDelta  = "content_block_delta"
	apiTestWebSocketTurnCompleted      = "turn.completed"
	apiTestWebSocketMetadataSessionID  = "sessionId"
	apiTestWebSocketMetadataRequestID  = "requestId"
	apiTestWebSocketReadTimeout        = 30 * time.Second
	apiTestWebSocketIsolationTimeout   = time.Second
	apiTestWebSocketInitialMessage     = "open a synchronized websocket session"
	apiTestWebSocketActiveMessage      = "hold this websocket turn active"
	apiTestWebSocketQueuedMessage      = "queue this websocket message"
)

type apiTestWebSocketResult struct {
	Queued bool `json:"queued"`
}

type apiTestWebSocketFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type apiTestWebSocketObservation struct {
	result          apiTestWebSocketResult
	sessionID       uuid.UUID
	requestID       uuid.UUID
	agentEventTypes []string
}

func TestAPIWebSocketSynchronizesSessionClientsAndQueues(t *testing.T) {
	sessionID := createAPIWebSocketSession(t, apiTestWebSocketInitialMessage)
	otherSessionID := createAPIWebSocketSession(t, "separate websocket session")

	first := dialAPIWebSocket(t, sessionID)
	t.Cleanup(func() { require.NoError(t, first.Close()) })
	second := dialAPIWebSocket(t, sessionID)
	t.Cleanup(func() { require.NoError(t, second.Close()) })
	other := dialAPIWebSocket(t, otherSessionID)
	t.Cleanup(func() { require.NoError(t, other.Close()) })

	hold, err := integrationInfra.HoldNextCompletion()
	require.NoError(t, err)
	t.Cleanup(hold.Release)

	require.NoError(t, first.WriteJSON(dabluveees.NewEvent(
		apiTestWebSocketMessageSend,
		map[string]string{"message": apiTestWebSocketActiveMessage},
	)))
	awaitAPIWebSocketProviderHold(t, hold)

	require.NoError(t, second.WriteJSON(dabluveees.NewEvent(
		apiTestWebSocketMessageSend,
		map[string]string{"message": apiTestWebSocketQueuedMessage},
	)))

	firstQueued := awaitAPIWebSocketCompletion(t, first, true)
	secondQueued := awaitAPIWebSocketCompletion(t, second, true)
	assert.Equal(t, firstQueued.result, secondQueued.result)
	assert.Equal(t, sessionID, firstQueued.sessionID)
	assert.NotEqual(t, uuid.Nil, firstQueued.requestID)

	hold.Release()

	firstCompleted := awaitAPIWebSocketCompletion(t, first, false)
	secondCompleted := awaitAPIWebSocketCompletion(t, second, false)
	assert.Equal(t, firstCompleted.result, secondCompleted.result)
	assert.Equal(t, sessionID, firstCompleted.sessionID)
	assert.NotEqual(t, uuid.Nil, firstCompleted.requestID)

	assertNoAPIWebSocketEvent(t, other)

	messages := listMessages(t, sessionID, 10, 0, "asc")
	contents := apiMessageContents(messages.Items)
	assert.Contains(t, contents, apiTestWebSocketInitialMessage)
	assert.Contains(t, contents, apiTestWebSocketActiveMessage)
	assert.Contains(t, contents, apiTestWebSocketQueuedMessage)
	assert.False(t, getSession(t, sessionID).ActiveTurn)
}

func TestAPIWebSocketRejectsUnauthenticatedAndCreatesPendingSessions(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		dialer := websocket.Dialer{
			HandshakeTimeout: requestTimeout,
		}
		connection, response, err := dialer.Dial(
			apiTestWebSocketURL(t, uuid.New()),
			nil,
		)
		require.Error(t, err)
		require.Nil(t, connection)
		require.NotNil(t, response)
		require.Equal(t, http.StatusUnauthorized, response.StatusCode)
		require.NoError(t, response.Body.Close())
	})

	t.Run("pending session", func(t *testing.T) {
		sessionID := uuid.New()
		result := sendAPIWebSocketMessage(
			t,
			sessionID,
			apiTestWebSocketInitialMessage,
		)

		assert.Equal(t, sessionID, result.sessionID)
		assert.False(t, result.result.Queued)
		assert.Equal(t, sessionID, getSession(t, sessionID).Id)
	})
}

func createAPIWebSocketSession(t *testing.T, message string) uuid.UUID {
	t.Helper()
	sessionID := uuid.New()
	result := sendAPIWebSocketMessage(t, sessionID, message)
	assert.False(t, result.result.Queued)

	return sessionID
}

func sendAPIWebSocketMessage(
	t *testing.T,
	sessionID uuid.UUID,
	message string,
) apiTestWebSocketObservation {
	t.Helper()

	connection := dialAPIWebSocket(t, sessionID)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	require.NoError(t, writeAPIWebSocketMessage(connection, message))

	result := awaitAPIWebSocketCompletion(t, connection, false)
	assert.Equal(t, sessionID, result.sessionID)

	return result
}

func writeAPIWebSocketMessage(
	connection *websocket.Conn,
	message string,
) error {
	return connection.WriteJSON(dabluveees.NewEvent(
		apiTestWebSocketMessageSend,
		map[string]string{"message": message},
	))
}

func dialAPIWebSocket(t *testing.T, sessionID uuid.UUID) *websocket.Conn {
	t.Helper()

	dialer := apiTestWebSocketDialer()
	connection, response, err := dialer.Dial(
		apiTestWebSocketURL(t, sessionID),
		nil,
	)
	if response != nil {
		t.Cleanup(func() { require.NoError(t, response.Body.Close()) })
	}
	require.NoError(t, err)
	require.Equal(t, apiTestWebSocketSubprotocol, connection.Subprotocol())

	return connection
}

func apiTestWebSocketDialer() websocket.Dialer {
	return websocket.Dialer{
		HandshakeTimeout: requestTimeout,
		Subprotocols: []string{
			apiTestWebSocketSubprotocol,
			apiTestWebSocketBearerPrefix + base64.RawURLEncoding.EncodeToString(
				[]byte(testinfra.TestAPIToken),
			),
		},
	}
}

func apiTestWebSocketURL(t *testing.T, sessionID uuid.UUID) string {
	t.Helper()

	endpoint, err := url.Parse(integrationInfra.APIURL(apiTestWebSocketPath))
	require.NoError(t, err)
	endpoint.Scheme = "ws"
	query := endpoint.Query()
	query.Set(apiTestWebSocketSessionIDParameter, sessionID.String())
	endpoint.RawQuery = query.Encode()

	return endpoint.String()
}

func awaitAPIWebSocketProviderHold(
	t *testing.T,
	hold *testinfra.CompletionHold,
) {
	t.Helper()

	select {
	case <-hold.Observed:
	case <-time.After(apiTestWebSocketReadTimeout):
		t.Fatal("WebSocket message did not reach the provider")
	}
}

func awaitAPIWebSocketCompletion(
	t *testing.T,
	connection *websocket.Conn,
	wantQueued bool,
) apiTestWebSocketObservation {
	t.Helper()

	deadline := time.Now().Add(apiTestWebSocketReadTimeout)
	agentEventTypes := make([]string, 0)
	for {
		require.NoError(t, connection.SetReadDeadline(deadline))

		received := dabluveees.Event{}
		require.NoError(t, connection.ReadJSON(&received))

		switch received.Type {
		case apiTestWebSocketMessageCompleted:
			result := apiTestWebSocketResult{}
			require.NoError(t, json.Unmarshal(received.Data, &result))
			if result.Queued == wantQueued {
				sessionID, requestID := apiTestWebSocketMetadata(t, received)
				return apiTestWebSocketObservation{
					result:          result,
					sessionID:       sessionID,
					requestID:       requestID,
					agentEventTypes: agentEventTypes,
				}
			}
		case apiTestWebSocketMessageFailed:
			t.Fatalf("WebSocket message failed: %s", received.Data)
		default:
			_, _ = apiTestWebSocketMetadata(t, received)
			agentEventTypes = append(agentEventTypes, string(received.Type))
		}
	}
}

func awaitAPIWebSocketFailure(
	t *testing.T,
	connection *websocket.Conn,
) apiTestWebSocketFailure {
	t.Helper()

	deadline := time.Now().Add(apiTestWebSocketReadTimeout)
	for {
		require.NoError(t, connection.SetReadDeadline(deadline))

		received := dabluveees.Event{}
		require.NoError(t, connection.ReadJSON(&received))
		switch received.Type {
		case apiTestWebSocketMessageFailed:
			failure := apiTestWebSocketFailure{}
			require.NoError(t, json.Unmarshal(received.Data, &failure))

			return failure
		case apiTestWebSocketMessageCompleted:
			t.Fatal("WebSocket message completed instead of failing")
		}
	}
}

func apiTestWebSocketMetadata(
	t *testing.T,
	event dabluveees.Event,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	require.NotNil(t, event.Metadata)
	sessionValue, found := event.Metadata.Get(apiTestWebSocketMetadataSessionID)
	require.True(t, found)
	sessionText, ok := sessionValue.(string)
	require.True(t, ok)
	sessionID, err := uuid.Parse(sessionText)
	require.NoError(t, err)

	requestValue, found := event.Metadata.Get(apiTestWebSocketMetadataRequestID)
	require.True(t, found)
	requestText, ok := requestValue.(string)
	require.True(t, ok)
	requestID, err := uuid.Parse(requestText)
	require.NoError(t, err)

	return sessionID, requestID
}

func assertNoAPIWebSocketEvent(t *testing.T, connection *websocket.Conn) {
	t.Helper()
	require.NoError(
		t,
		connection.SetReadDeadline(
			time.Now().Add(apiTestWebSocketIsolationTimeout),
		),
	)

	received := dabluveees.Event{}
	err := connection.ReadJSON(&received)
	require.Error(t, err)

	networkErr, isNetworkErr := err.(net.Error)
	require.True(t, isNetworkErr)
	assert.True(t, networkErr.Timeout())
}
