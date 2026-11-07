//go:build integration

package api_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	"github.com/psyb0t/peen/internal/pkg/http/wsapi"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// awaitAPIWebSocketEventOfType reads frames off connection until one of type
// eventType arrives, failing the test on a message.failed in between.
func awaitAPIWebSocketEventOfType(
	t *testing.T,
	connection *websocket.Conn,
	eventType string,
) dabluveees.Event {
	t.Helper()

	for {
		event := readWebSocketEvent(t, connection)
		if string(event.Type) == apiTestWebSocketMessageFailed {
			t.Fatalf("WebSocket message failed: %s", event.Data)
		}

		if string(event.Type) == eventType {
			return event
		}
	}
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

	// Filter to the session the message goes to. A global socket also gets
	// other sessions' frames, so a completion from an earlier test's turn
	// that ends just as this one starts would be mistaken for this reply.
	sessionID := openAPIWorkspaceSession(t)
	connection := dialAPIWebSocket(t, &sessionID)
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
		t,
		connection,
		openAPIWorkspaceSession(t),
		data,
	)
}

// writeAPIWebSocketSessionMessage builds and sends one message.send, failing
// the test immediately when the built frame does not decode through the
// generated wsapi.MessageSend contract. This is the one place every
// message.send a test sends goes through, so a stale field name or a shape
// the spec no longer accepts is caught here rather than surfacing as an
// unrelated failure further down a test.
func writeAPIWebSocketSessionMessage(
	t *testing.T,
	connection *websocket.Conn,
	sessionID uuid.UUID,
	data map[string]string,
) error {
	t.Helper()

	event := dabluveees.NewEvent(
		apiTestWebSocketMessageSend,
		data,
	).SetMetadata(
		apiTestWebSocketSessionIDParameter,
		sessionID.String(),
	)

	assertValidMessageSend(t, event)

	return connection.WriteJSON(event)
}

// writeAPIWebSocketSessionMessageUnvalidated sends message.send without the
// wsapi.MessageSend contract check writeAPIWebSocketSessionMessage normally
// runs. Its only caller deliberately sends a reasoningEffort the contract
// itself now also closes off, to prove the live server still rejects it;
// validating that frame before it is written would catch the bad value
// locally and the test would never reach the server at all.
func writeAPIWebSocketSessionMessageUnvalidated(
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

// assertValidMessageSend fails the test immediately, naming the frame type
// and the validation error, when a message.send a test is about to write
// does not decode through the generated wsapi contract.
func assertValidMessageSend(t *testing.T, event dabluveees.Event) {
	t.Helper()

	encoded, err := json.Marshal(event)
	require.NoError(t, err)

	target := wsapi.MessageSend{}
	require.NoErrorf(
		t,
		target.UnmarshalJSON(encoded),
		"%s failed the wsapi.MessageSend contract: %s",
		apiTestWebSocketMessageSend,
		encoded,
	)
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

	return awaitAPIWebSocketCompletionForEvent(t, connection, wantQueued,
		uuid.Nil)
}

func awaitAPIWebSocketCompletionForEvent(
	t *testing.T,
	connection *websocket.Conn,
	wantQueued bool,
	triggeringEventID uuid.UUID,
) apiTestWebSocketObservation {
	t.Helper()

	deadline := time.Now().Add(apiTestWebSocketReadTimeout)
	agentEventTypes := make([]string, 0)
	deliveredRequestIDs := make([]uuid.UUID, 0)
	for {
		require.NoError(t, connection.SetReadDeadline(deadline))

		received := dabluveees.Event{}
		require.NoError(t, connection.ReadJSON(&received))
		assertValidServerFrame(t, received)

		if triggeringEventID != uuid.Nil && (received.TriggeredBy == nil ||
			*received.TriggeredBy != triggeringEventID) {
			continue
		}

		switch string(received.Type) {
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
		assertValidServerFrame(t, received)

		switch string(received.Type) {
		case apiTestWebSocketMessageFailed:
			failure := apiTestWebSocketFailure{}
			require.NoError(t, json.Unmarshal(received.Data, &failure))

			return failure
		case apiTestWebSocketMessageCompleted:
			t.Fatal("WebSocket message completed instead of failing")
		}
	}
}

// apiTestWebSocketMetadata reads metadata.sessionId, required on every
// session frame, and metadata.requestId, which an agent.run.* or
// notice.created frame carries no value for because it belongs to no one
// turn's request. A frame missing it returns uuid.Nil for the request ID.
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

	requestID := uuid.Nil
	if requestValue, found := event.Metadata.Get(
		apiTestWebSocketMetadataRequestID,
	); found {
		requestText, ok := requestValue.(string)
		require.True(t, ok)
		requestID, err = uuid.Parse(requestText)
		require.NoError(t, err)
	}

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

	var networkErr net.Error

	isNetworkErr := errors.As(err, &networkErr)
	require.True(t, isNetworkErr)
	assert.True(t, networkErr.Timeout())
}
