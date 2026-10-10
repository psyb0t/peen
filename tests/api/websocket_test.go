//go:build integration

package api_test

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"path"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/psyb0t/aichteeteapee"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	"github.com/psyb0t/peen/internal/pkg/http/api"
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
	apiTestWebSocketMessageDelivered   = "user_message.delivered"
	apiTestWebSocketMetadataSessionID  = "sessionId"
	apiTestWebSocketMetadataRequestID  = "requestId"
	apiTestWebSocketReadTimeout        = 30 * time.Second
	apiTestWebSocketIsolationTimeout   = time.Second
	apiTestWebSocketInitialMessage     = "open a synchronized websocket session"
	apiTestWebSocketActiveMessage      = "hold this websocket turn active"
	apiTestWebSocketQueuedMessage      = "queue this websocket message"
	apiTestWebSocketReasoningMessage   = "think about this websocket message"
	apiTestWebSocketReasoningEffort    = "high"

	apiTestWebSocketOrderedWorkspacePrefix = "admission-order-"
	apiTestWebSocketOrderedFixtureFile     = "README.md"
	apiTestWebSocketOrderedFixture         = "a workspace no test has opened yet"
	apiTestWebSocketFirstOrderedMessage    = "run the first ordered message"
	apiTestWebSocketSecondOrderedMessage   = "then answer the second ordered message"
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
	// deliveredRequestIDs lists the request IDs of queued messages that
	// reached the model before this completion arrived.
	deliveredRequestIDs []uuid.UUID
}

func TestAPIWebSocketBroadcastsGlobalEventsAndQueues(t *testing.T) {
	sessionID := createAPIWebSocketSession(t, apiTestWebSocketInitialMessage)
	filteredOutSessionID := uuid.New()

	first := dialAPIWebSocket(t, nil)
	t.Cleanup(func() { require.NoError(t, first.Close()) })
	second := dialAPIWebSocket(t, nil)
	t.Cleanup(func() { require.NoError(t, second.Close()) })
	observer := dialAPIWebSocket(t, nil)
	t.Cleanup(func() { require.NoError(t, observer.Close()) })
	filteredOther := dialAPIWebSocket(t, &filteredOutSessionID)
	t.Cleanup(func() { require.NoError(t, filteredOther.Close()) })

	hold, err := integrationInfra.HoldNextCompletion()
	require.NoError(t, err)
	t.Cleanup(hold.Release)

	require.NoError(t, writeAPIWebSocketMessage(
		t,
		first,
		apiTestWebSocketActiveMessage,
	))
	awaitAPIWebSocketProviderHold(t, hold)

	require.NoError(t, writeAPIWebSocketMessage(
		t,
		second,
		apiTestWebSocketQueuedMessage,
	))

	firstQueued := awaitAPIWebSocketCompletion(t, first, true)
	secondQueued := awaitAPIWebSocketCompletion(t, second, true)
	assert.Equal(t, firstQueued.result, secondQueued.result)
	observerQueued := awaitAPIWebSocketCompletion(t, observer, true)
	assert.Equal(t, firstQueued.result, observerQueued.result)
	assert.Equal(t, sessionID, firstQueued.sessionID)
	assert.NotEqual(t, uuid.Nil, firstQueued.requestID)

	hold.Release()

	firstCompleted := awaitAPIWebSocketCompletion(t, first, false)
	secondCompleted := awaitAPIWebSocketCompletion(t, second, false)
	assert.Equal(t, firstCompleted.result, secondCompleted.result)
	observerCompleted := awaitAPIWebSocketCompletion(t, observer, false)
	assert.Equal(t, firstCompleted.result, observerCompleted.result)
	assert.Equal(t, sessionID, firstCompleted.sessionID)
	assert.NotEqual(t, uuid.Nil, firstCompleted.requestID)

	// Every client sees the queued message land, under the request ID the
	// sender got back, so any of them can mark it as delivered.
	queuedRequestID := []uuid.UUID{secondQueued.requestID}
	assert.Equal(t, queuedRequestID, firstCompleted.deliveredRequestIDs)
	assert.Equal(t, queuedRequestID, secondCompleted.deliveredRequestIDs)
	assert.Equal(t, queuedRequestID, observerCompleted.deliveredRequestIDs)

	assertNoAPIWebSocketEvent(t, filteredOther)

	messages := collectAllMessages(t, sessionID)
	contents := apiMessageContents(messages)
	assert.Contains(t, contents, apiTestWebSocketInitialMessage)
	assert.Contains(t, contents, apiTestWebSocketActiveMessage)
	assert.Contains(t, contents, apiTestWebSocketQueuedMessage)
	assert.False(t, getSession(t, sessionID).ActiveTurn)
}

// A control surface sends the selected model and reasoning with every message.
// Two such messages sent back to back while the session's worker is still
// starting must be admitted in the order they were sent: the first starts the
// turn, the second joins that turn's queue, and neither is refused.
func TestAPIWebSocketAdmitsMessagesInOrderWhileTheWorkerStarts(t *testing.T) {
	directory := apiTestWebSocketOrderedWorkspacePrefix + uuid.NewString()
	require.NoError(t, integrationInfra.WriteWorkspaceFile(
		t.Context(),
		path.Join(directory, apiTestWebSocketOrderedFixtureFile),
		[]byte(apiTestWebSocketOrderedFixture),
	))

	sessionID := openAPIWorkspaceSessionAt(
		t,
		path.Join(testinfra.ContainerWorkingDirectory, directory),
	)

	connection := dialAPIWebSocket(t, &sessionID)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	hold, err := integrationInfra.HoldNextCompletion()
	require.NoError(t, err)
	t.Cleanup(hold.Release)

	for _, message := range []string{
		apiTestWebSocketFirstOrderedMessage,
		apiTestWebSocketSecondOrderedMessage,
	} {
		require.NoError(t, writeAPIWebSocketSessionMessage(
			connection,
			sessionID,
			map[string]string{
				"message":         message,
				"model":           integrationInfra.DefaultModel(),
				"reasoningEffort": apiTestWebSocketReasoningEffort,
			},
		))
	}

	// Reading a completion fails the test on any message.failed, so reaching
	// each one also proves neither message was refused.
	queued := awaitAPIWebSocketCompletion(t, connection, true)
	awaitAPIWebSocketProviderHold(t, hold)
	hold.Release()

	completed := awaitAPIWebSocketCompletion(t, connection, false)
	assert.NotEqual(t, queued.requestID, completed.requestID)
	assert.Equal(
		t,
		[]uuid.UUID{queued.requestID},
		completed.deliveredRequestIDs,
	)

	userMessages := make([]string, 0)
	for _, message := range collectAllMessages(t, sessionID) {
		if message.Role != api.MessageRoleUser {
			continue
		}

		userMessages = append(userMessages, message.Content)
	}

	assert.Equal(
		t,
		[]string{
			apiTestWebSocketFirstOrderedMessage,
			apiTestWebSocketSecondOrderedMessage,
		},
		userMessages,
	)
}

func TestAPIWebSocketRejectsUnauthenticatedAndRoutesToTheOpenedSession(
	t *testing.T,
) {
	t.Run("unauthenticated", func(t *testing.T) {
		dialer := websocket.Dialer{
			HandshakeTimeout: requestTimeout,
		}
		connection, response, err := dialer.Dial(
			apiTestWebSocketURL(t, nil),
			nil,
		)
		require.Error(t, err)
		require.Nil(t, connection)
		require.NotNil(t, response)
		require.Equal(t, http.StatusUnauthorized, response.StatusCode)
		require.NoError(t, response.Body.Close())
	})

	t.Run("opened session", func(t *testing.T) {
		result := sendAPIWebSocketMessage(t, apiTestWebSocketInitialMessage)

		assert.NotEqual(t, uuid.Nil, result.sessionID)
		assert.False(t, result.result.Queued)
		assert.Equal(t, result.sessionID, getSession(t, result.sessionID).Id)
	})
}

func TestAPIWebSocketCarriesTheTurnReasoningEffort(t *testing.T) {
	t.Run("requested level reaches the provider", func(t *testing.T) {
		connection := dialAPIWebSocket(t, nil)
		t.Cleanup(func() { require.NoError(t, connection.Close()) })

		require.NoError(t, writeAPIWebSocketMessageData(t, connection, map[string]string{
			"message":         apiTestWebSocketReasoningMessage,
			"reasoningEffort": apiTestWebSocketReasoningEffort,
		}))
		awaitAPIWebSocketCompletion(t, connection, false)

		assert.Equal(
			t,
			apiTestWebSocketReasoningEffort,
			integrationInfra.LastReasoningEffort(),
		)
	})

	t.Run("no level leaves the provider default", func(t *testing.T) {
		sendAPIWebSocketMessage(t, apiTestWebSocketReasoningMessage)

		assert.Empty(t, integrationInfra.LastReasoningEffort())
	})

	t.Run("unknown level is rejected", func(t *testing.T) {
		connection := dialAPIWebSocket(t, nil)
		t.Cleanup(func() { require.NoError(t, connection.Close()) })

		require.NoError(t, writeAPIWebSocketMessageData(t, connection, map[string]string{
			"message":         apiTestWebSocketReasoningMessage,
			"reasoningEffort": "extreme",
		}))

		failure := awaitAPIWebSocketFailure(t, connection)
		assert.Equal(t, string(aichteeteapee.ErrorCodeValidationFailed), failure.Code)
	})
}

func createAPIWebSocketSession(t *testing.T, message string) uuid.UUID {
	t.Helper()
	result := sendAPIWebSocketMessage(t, message)
	assert.False(t, result.result.Queued)

	return result.sessionID
}

func sendAPIWebSocketMessage(
	t *testing.T,
	message string,
) apiTestWebSocketObservation {
	t.Helper()

	connection := dialAPIWebSocket(t, nil)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	require.NoError(t, writeAPIWebSocketMessage(t, connection, message))

	result := awaitAPIWebSocketCompletion(t, connection, false)
	assert.NotEqual(t, uuid.Nil, result.sessionID)

	return result
}

func writeAPIWebSocketMessage(
	t *testing.T,
	connection *websocket.Conn,
	message string,
) error {
	t.Helper()

	return writeAPIWebSocketMessageData(
		t,
		connection,
		map[string]string{"message": message},
	)
}

func writeAPIWebSocketMessageData(
	t *testing.T,
	connection *websocket.Conn,
	data map[string]string,
) error {
	t.Helper()

	return writeAPIWebSocketSessionMessage(
		connection,
		openAPIWorkspaceSession(t),
		data,
	)
}

func writeAPIWebSocketSessionMessage(
	connection *websocket.Conn,
	sessionID uuid.UUID,
	data map[string]string,
) error {
	event := dabluveees.NewEvent(
		apiTestWebSocketMessageSend,
		data,
	).SetMetadata(
		apiTestWebSocketSessionIDParameter,
		sessionID.String(),
	)

	return connection.WriteJSON(event)
}

// openAPIWorkspaceSession opens the container's workspace through the real
// control endpoint. The service starts with no sessions, so every turn these
// tests run begins here. Opening the same workspace again resumes it, so
// calling this per message keeps returning the one session.
func openAPIWorkspaceSession(t *testing.T) uuid.UUID {
	t.Helper()

	return openAPIWorkspaceSessionAt(t, testinfra.ContainerWorkingDirectory)
}

// openAPIWorkspaceSessionAt opens one workspace below the container's root.
// A workspace no test has opened yet gets a session with no worker, which is
// how a test sends messages while that worker is still starting.
func openAPIWorkspaceSessionAt(t *testing.T, workspace string) uuid.UUID {
	t.Helper()

	body, err := json.Marshal(map[string]string{
		"workspace": workspace,
	})
	require.NoError(t, err)

	response := apiRequest(
		t,
		http.MethodPost,
		apiBasePath+"/sessions/open",
		body,
		map[string]string{
			headerContentType:   jsonMediaType,
			headerAuthorization: bearerPrefix + testinfra.TestAPIToken,
		},
	)
	require.Equal(t, http.StatusOK, response.StatusCode)

	opened := decodeResponse[apiTestOpenedSession](t, response)
	require.Equal(t, workspace, opened.Session.Workspace)

	return opened.Session.ID
}

type apiTestOpenedSession struct {
	Created bool `json:"created"`
	Session struct {
		ID        uuid.UUID `json:"id"`
		Workspace string    `json:"workspace"`
	} `json:"session"`
}

func dialAPIWebSocket(
	t *testing.T,
	filterSessionID *uuid.UUID,
) *websocket.Conn {
	t.Helper()

	dialer := apiTestWebSocketDialer()
	connection, response, err := dialer.Dial(
		apiTestWebSocketURL(t, filterSessionID),
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

func apiTestWebSocketURL(
	t *testing.T,
	filterSessionID *uuid.UUID,
) string {
	t.Helper()

	endpoint, err := url.Parse(integrationInfra.APIURL(apiTestWebSocketPath))
	require.NoError(t, err)
	endpoint.Scheme = "ws"
	query := endpoint.Query()
	if filterSessionID != nil {
		query.Set(
			apiTestWebSocketSessionIDParameter,
			filterSessionID.String(),
		)
	}
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
	deliveredRequestIDs := make([]uuid.UUID, 0)
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
					result:              result,
					sessionID:           sessionID,
					requestID:           requestID,
					agentEventTypes:     agentEventTypes,
					deliveredRequestIDs: deliveredRequestIDs,
				}
			}
		case apiTestWebSocketMessageFailed:
			t.Fatalf("WebSocket message failed: %s", received.Data)
		default:
			_, requestID := apiTestWebSocketMetadata(t, received)
			agentEventTypes = append(agentEventTypes, string(received.Type))
			if received.Type == apiTestWebSocketMessageDelivered {
				deliveredRequestIDs = append(deliveredRequestIDs, requestID)
			}
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
