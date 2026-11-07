//go:build integration

package api_test

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	server "github.com/psyb0t/peen/internal/pkg/http/server"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPIMessageSessionAndPagination(t *testing.T) {
	require.True(t, integrationInfra.ModelDiscoveryObserved())

	requestMessages := []string{
		"first request",
		"second request",
		"third request",
	}
	var sessionID uuid.UUID

	for _, message := range requestMessages {
		result := sendAPIWebSocketMessage(t, message)
		assert.False(t, result.result.Queued)
		if sessionID == uuid.Nil {
			sessionID = result.sessionID

			continue
		}
		assert.Equal(t, sessionID, result.sessionID)
	}
	allMessages := collectAllMessages(t, sessionID)
	require.GreaterOrEqual(t, len(allMessages), len(requestMessages)*2)
	startOffset := len(allMessages) - len(requestMessages)*2
	newMessages := allMessages[startOffset:]
	expectedContents := apiMessageContents(newMessages)
	for index, message := range requestMessages {
		assert.Equal(t, message, expectedContents[index*2])
		assert.NotEmpty(t, expectedContents[index*2+1])
	}

	session := getSession(t, sessionID)
	assert.Equal(t, sessionID, session.Id)
	assert.GreaterOrEqual(t, session.MessageCount, int64(len(expectedContents)))
	assert.GreaterOrEqual(t, session.CompletedTurnCount, int64(len(requestMessages)))
	assert.False(t, session.ActiveTurn)
	assert.NotNil(t, session.LastMessageAt)
	assert.False(t, session.CreatedAt.IsZero())
	assert.False(t, session.UpdatedAt.IsZero())

	pageTestCases := []struct {
		name    string
		offset  int
		want    []string
		hasMore bool
	}{
		{
			name:    "first page",
			offset:  0,
			want:    expectedContents[:2],
			hasMore: true,
		},
		{
			name:    "second page",
			offset:  2,
			want:    expectedContents[2:4],
			hasMore: true,
		},
		{
			name:    "final page",
			offset:  4,
			want:    expectedContents[4:],
			hasMore: false,
		},
		{
			name:    "empty exact end page",
			offset:  len(expectedContents),
			want:    []string{},
			hasMore: false,
		},
		{
			name:    "empty far past page",
			offset:  len(expectedContents) + 100,
			want:    []string{},
			hasMore: false,
		},
	}

	for _, tc := range pageTestCases {
		t.Run(tc.name, func(t *testing.T) {
			page := listMessages(t, sessionID, 2, startOffset+tc.offset, "asc")

			assert.Equal(t, int32(2), page.Limit)
			assert.EqualValues(t, startOffset+tc.offset, page.Offset)
			assert.Equal(t, tc.hasMore, page.HasMore)
			assert.Equal(t, tc.want, apiMessageContents(page.Items))
		})
	}

	descending := listMessages(t, sessionID, len(expectedContents), 0, "desc")
	wantDescending := slices.Clone(expectedContents)
	slices.Reverse(wantDescending)
	assert.Equal(t, wantDescending, apiMessageContents(descending.Items))
	assert.Equal(t, startOffset > 0, descending.HasMore)
}

func TestAPIWebSocketStreamsAndPersistsTurn(t *testing.T) {
	observation := sendAPIWebSocketMessage(t, "stream this request")
	sessionID := observation.sessionID
	assert.Contains(
		t,
		observation.agentEventTypes,
		apiTestWebSocketTurnStarted,
	)
	assert.Contains(
		t,
		observation.agentEventTypes,
		apiTestWebSocketContentBlockDelta,
	)
	assert.Contains(
		t,
		observation.agentEventTypes,
		apiTestWebSocketTurnCompleted,
	)

	messages := collectAllMessages(t, sessionID)
	require.GreaterOrEqual(t, len(messages), 2)
	lastTurn := messages[len(messages)-2:]
	assert.Equal(t, "stream this request", lastTurn[0].Content)
	assert.NotEmpty(t, lastTurn[1].Content)
	require.NotNil(t, lastTurn[1].Thinking)
	assert.Equal(t, testinfra.DefaultProviderReasoning, *lastTurn[1].Thinking)
}

func TestAPIReplaysDurableModelAudit(t *testing.T) {
	observation := sendAPIWebSocketMessage(t, "record the provider exchange")
	sessionID := observation.sessionID

	runs := listModelRuns(t, sessionID, 10, 0)
	require.NotEmpty(t, runs.ModelRuns)
	run := runs.ModelRuns[0]
	assert.Equal(t, sessionID, run.SessionId)
	assert.Equal(t, api.ModelRunStageTurn, run.Stage)
	assert.NotEmpty(t, run.ConnectionName)
	assert.NotEmpty(t, run.ModelReference)
	assert.NotEmpty(t, run.RequestedModelId)
	assert.NotEmpty(t, run.RequestSettings)
	assert.NotEmpty(t, run.ResponseMessages)
	assert.NotEmpty(t, run.ResponseUsage)
	assert.False(t, run.StartedAt.IsZero())

	calls := listModelRunCalls(t, sessionID, run.Id, 10, 0)
	require.Len(t, calls.Calls, 1)
	assert.Equal(t, run.Id, calls.ModelRun.Id)
	assert.Equal(t, int64(0), calls.Calls[0].Round)
	assert.NotEmpty(t, calls.Calls[0].RequestMessages)
	assert.NotEmpty(t, calls.Calls[0].ResponseUsage)
	assert.False(t, calls.Calls[0].StartedAt.IsZero())
}

func TestAPICancelsActiveTurn(t *testing.T) {
	sessionID := createAPIWebSocketSession(t, "create cancellable session")

	hold, err := integrationInfra.HoldNextCompletion()
	require.NoError(t, err)
	t.Cleanup(hold.Release)

	connection := dialAPIWebSocket(t, nil)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	require.NoError(t, writeAPIWebSocketMessage(
		t,
		connection,
		"block this request",
	))

	select {
	case <-hold.Observed:
	case <-time.After(requestTimeout):
		t.Fatal("provider mock did not receive the blocked completion")
	}

	require.Eventually(t, func() bool {
		return getSession(t, sessionID).ActiveTurn
	}, requestTimeout, 10*time.Millisecond)

	cancelResponse := apiRequest(
		t,
		http.MethodPost,
		sessionCancelPath,
		nil,
		withHeader(authenticatedHeaders(), headerSessionID, sessionID.String()),
	)
	require.Equal(t, http.StatusAccepted, cancelResponse.StatusCode)
	cancelResult := decodeResponse[api.CancelResponse](t, cancelResponse)
	assert.True(t, cancelResult.CancelRequested)

	failure := awaitAPIWebSocketFailure(t, connection)
	assert.Equal(t, server.ErrorCodeTurnCancelled, failure.Code)

	require.Eventually(t, func() bool {
		return !getSession(t, sessionID).ActiveTurn
	}, requestTimeout, 10*time.Millisecond)
}

func TestAPIQueuesMessageForActiveTurn(t *testing.T) {
	sessionID := createAPIWebSocketSession(t, apiTestQueuedSessionMessage)

	hold, err := integrationInfra.HoldNextCompletion()
	require.NoError(t, err)
	t.Cleanup(hold.Release)

	connection := dialAPIWebSocket(t, nil)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	require.NoError(t, writeAPIWebSocketMessage(
		t,
		connection,
		apiTestActiveTurnMessage,
	))

	select {
	case <-hold.Observed:
	case <-time.After(requestTimeout):
		t.Fatal("provider mock did not receive the active turn")
	}

	require.Eventually(t, func() bool {
		return getSession(t, sessionID).ActiveTurn
	}, requestTimeout, 10*time.Millisecond)

	queuedConnection := dialAPIWebSocket(t, nil)
	t.Cleanup(func() { require.NoError(t, queuedConnection.Close()) })
	require.NoError(t, writeAPIWebSocketMessage(
		t,
		queuedConnection,
		apiTestQueuedMessage,
	))

	activeQueued := awaitAPIWebSocketCompletion(t, connection, true)
	queued := awaitAPIWebSocketCompletion(t, queuedConnection, true)
	assert.Equal(t, activeQueued.result, queued.result)
	assert.True(t, queued.result.Queued)

	hold.Release()

	activeCompleted := awaitAPIWebSocketCompletion(t, connection, false)
	queuedCompleted := awaitAPIWebSocketCompletion(t, queuedConnection, false)
	assert.Equal(t, activeCompleted.result, queuedCompleted.result)
	assert.False(t, activeCompleted.result.Queued)

	messages := collectAllMessages(t, sessionID)
	require.GreaterOrEqual(t, len(messages), apiTestQueuedPageLimit)
	lastTurn := messages[len(messages)-apiTestQueuedPageLimit:]
	assert.Equal(t, apiTestActiveTurnMessage, lastTurn[0].Content)
	assert.Equal(t, apiTestQueuedMessage, lastTurn[2].Content)
	assert.Equal(t, api.MessageRoleAssistant, lastTurn[1].Role)
	assert.Equal(t, api.MessageRoleAssistant, lastTurn[3].Role)
}

func TestAPIDoesNotSubmitMessagesOverHTTP(t *testing.T) {
	response := apiRequest(
		t,
		http.MethodPost,
		messagesPath,
		nil,
		authenticatedHeaders(),
	)
	require.Equal(t, http.StatusMethodNotAllowed, response.StatusCode)
}
