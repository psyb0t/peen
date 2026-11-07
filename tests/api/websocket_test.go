//go:build integration

package api_test

import (
	"context"
	"net/http"
	"path"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A turn runs to completion even though every socket attached to its session
// closes while the provider request is still in flight. Only explicit Stop,
// turn failure, or a crash stop work now; a disconnect does none of those.
func TestAPIWebSocketTurnFinishesWithNoClientAttached(t *testing.T) {
	workspace := openFreshWorkspace(
		t,
		apiTestWebSocketUnattendedWorkspacePrefix,
		nil,
	)
	sessionID := workspace.sessionID
	connection := dialAPIWebSocket(t, &sessionID)
	observer := dialAPIWebSocket(t, &sessionID)

	hold, err := integrationInfra.HoldNextCompletion()
	require.NoError(t, err)
	t.Cleanup(hold.Release)
	sendSessionMessage(t, connection, sessionID, apiTestWebSocketUnattendedMessage)
	awaitAPIWebSocketProviderHold(t, hold)

	require.NoError(t, connection.Close())
	require.NoError(t, observer.Close())

	assert.Never(t, func() bool {
		select {
		case <-hold.Cancelled:
			return true
		default:
			return false
		}
	}, apiTestWebSocketIsolationTimeout, 10*time.Millisecond,
		"closing every session socket must not cancel the provider request")

	hold.Release()

	require.Eventually(t, func() bool {
		return !getSession(t, sessionID).ActiveTurn
	}, requestTimeout, 10*time.Millisecond)

	messages := collectAllMessages(t, sessionID)
	contents := apiMessageContents(messages)
	assert.Contains(t, contents, apiTestWebSocketUnattendedMessage)
	require.NotEmpty(t, messages)
	lastMessage := messages[len(messages)-1]
	assert.Equal(t, api.MessageRoleAssistant, lastMessage.Role)
	assert.NotEmpty(t, lastMessage.Content)

	feed := collectSessionFeed(t, sessionID, 0)
	feedTypes := make([]string, 0, len(feed))
	for _, item := range feed {
		feedTypes = append(feedTypes, item.Type)
	}
	assert.Contains(t, feedTypes, apiTestWebSocketTurnCompleted)
}

// A client disconnecting from a session with other clients still attached
// must not disturb the turn those other clients see, the same as it would
// not disturb it with nobody attached at all.
func TestAPIWebSocketOneClientDisconnectingDoesNotAffectAnother(t *testing.T) {
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		logs, err := integrationInfra.AppLogs(ctx)
		require.NoError(t, err)
		t.Logf("continuity failure container logs: %s", logs)
	})
	sessionID := createAPIWebSocketSession(t, "open shared client session")
	first := dialAPIWebSocket(t, &sessionID)
	second := dialAPIWebSocket(t, &sessionID)
	t.Cleanup(func() { require.NoError(t, second.Close()) })
	hold, err := integrationInfra.HoldNextCompletion()
	require.NoError(t, err)
	t.Cleanup(hold.Release)
	require.NoError(t, writeAPIWebSocketMessage(t, first,
		"keep running while another session client remains"))
	awaitAPIWebSocketProviderHold(t, hold)
	require.NoError(t, first.Close())
	hold.Release()
	completed := awaitAPIWebSocketCompletion(t, second, false)
	assert.Equal(t, sessionID, completed.sessionID)
	select {
	case <-hold.Cancelled:
		t.Fatal("disconnect cancelled work with another session client present")
	default:
	}
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
			t,
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

		// Sent through the unvalidated path on purpose: wsapi.MessageSend's
		// reasoningEffort enum would also reject "extreme" locally, which
		// would never let this reach the live server. This subtest proves
		// the running server still answers with its own VALIDATION_FAILED
		// message.failed for the same input.
		require.NoError(t, writeAPIWebSocketSessionMessageUnvalidated(
			connection,
			openAPIWorkspaceSession(t),
			map[string]string{
				"message":         apiTestWebSocketReasoningMessage,
				"reasoningEffort": "extreme",
			},
		))

		failure := awaitAPIWebSocketFailure(t, connection)
		assert.Equal(t, aichteeteapee.ErrorCodeValidationFailed, failure.Code)
	})
}
