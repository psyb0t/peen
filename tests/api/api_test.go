//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	server "github.com/psyb0t/peen/internal/pkg/http/server"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	apiBasePath           = "/v1"
	messagesPath          = apiBasePath + "/messages"
	sessionPath           = apiBasePath + "/session"
	sessionCancelPath     = sessionPath + "/cancel"
	headerAuthorization   = "Authorization"
	headerContentType     = "Content-Type"
	headerSessionID       = "X-Session-ID"
	bearerPrefix          = "Bearer "
	jsonMediaType         = "application/json"
	integrationTimeout    = 5 * time.Minute
	requestTimeout        = 30 * time.Second
	apiTestRestartTimeout = 30 * time.Second

	apiTestWorkspaceRulesFile   = "AGENTS.md"
	apiTestInitialRules         = "Initial image workspace rules."
	apiTestUpdatedRules         = "Updated image workspace rules."
	apiTestRestartMessage       = "prove durable image history"
	apiTestReloadMessage        = "prove image harness reload"
	apiTestQueuedSessionMessage = "create a queueable session"
	apiTestActiveTurnMessage    = "hold this active turn"
	apiTestQueuedMessage        = "queue this message for the active turn"
	apiTestQueuedPageLimit      = 4
	apiTestQueuedPageOffset     = 2
)

var integrationInfra *testinfra.Infra

func TestMain(m *testing.M) {
	setupContext, cancelSetup := context.WithTimeout(
		context.Background(),
		integrationTimeout,
	)

	infra, err := testinfra.Setup(setupContext)
	cancelSetup()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "set up Peen API integration test:", err)
		os.Exit(1)
	}
	integrationInfra = infra

	exitCode := m.Run()
	teardownContext, cancelTeardown := context.WithTimeout(
		context.Background(),
		integrationTimeout,
	)
	if err := integrationInfra.Teardown(teardownContext); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "tear down Peen API integration test:", err)
		if exitCode == 0 {
			exitCode = 1
		}
	}
	cancelTeardown()

	os.Exit(exitCode)
}

func TestAPIMessageSessionAndPagination(t *testing.T) {
	require.True(t, integrationInfra.ModelDiscoveryObserved())

	requestMessages := []string{
		"first request",
		"second request",
		"third request",
	}
	sessionID := uuid.New()

	for _, message := range requestMessages {
		result := sendAPIWebSocketMessage(t, sessionID, message)
		assert.False(t, result.result.Queued)
	}
	allMessages := listMessages(t, sessionID, 10, 0, "asc")
	expectedContents := apiMessageContents(allMessages.Items)
	require.Len(t, expectedContents, len(requestMessages)*2)
	for index, message := range requestMessages {
		assert.Equal(t, message, expectedContents[index*2])
		assert.NotEmpty(t, expectedContents[index*2+1])
	}

	session := getSession(t, sessionID)
	assert.Equal(t, sessionID, session.Id)
	assert.Equal(t, int64(len(expectedContents)), session.MessageCount)
	assert.Equal(t, int64(len(requestMessages)), session.CompletedTurnCount)
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
			page := listMessages(t, sessionID, 2, tc.offset, "asc")

			assert.Equal(t, int32(2), page.Limit)
			assert.Equal(t, int32(tc.offset), page.Offset)
			assert.Equal(t, tc.hasMore, page.HasMore)
			assert.Equal(t, tc.want, apiMessageContents(page.Items))
		})
	}

	descending := listMessages(t, sessionID, len(expectedContents), 0, "desc")
	wantDescending := slices.Clone(expectedContents)
	slices.Reverse(wantDescending)
	assert.Equal(t, wantDescending, apiMessageContents(descending.Items))
	assert.False(t, descending.HasMore)
}

func TestAPIWebSocketStreamsAndPersistsTurn(t *testing.T) {
	sessionID := uuid.New()
	observation := sendAPIWebSocketMessage(t, sessionID, "stream this request")
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

	page := listMessages(t, sessionID, 10, 0, "asc")
	require.Len(t, page.Items, 2)
	assert.Equal(t, "stream this request", page.Items[0].Content)
	assert.NotEmpty(t, page.Items[1].Content)
	require.NotNil(t, page.Items[1].Thinking)
	assert.Equal(t, testinfra.DefaultProviderReasoning, *page.Items[1].Thinking)
}

func TestMetricsAreOnlyAvailableOnThePrivateListener(t *testing.T) {
	publicResponse := apiRequest(
		t,
		http.MethodGet,
		"/metrics",
		nil,
		authenticatedHeaders(),
	)
	requireAPIStatus(t, publicResponse, http.StatusNotFound)

	privateResponse, err := integrationInfra.HTTPClient().Get(
		integrationInfra.MetricsURL("/metrics"),
	)
	require.NoError(t, err)
	metricsBody, readErr := io.ReadAll(privateResponse.Body)
	closeErr := privateResponse.Body.Close()
	require.NoError(t, readErr)
	require.NoError(t, closeErr)
	require.Equal(t, http.StatusOK, privateResponse.StatusCode)
	assert.Contains(t, string(metricsBody), "peen_http_requests_total")
}

func TestProductionImageRestartsWithDurableStateAndFreshHarness(t *testing.T) {
	require.NoError(t, integrationInfra.WriteWorkspaceFile(
		t.Context(),
		apiTestWorkspaceRulesFile,
		[]byte(apiTestInitialRules),
	))

	sessionID := uuid.New()
	_ = sendAPIWebSocketMessage(t, sessionID, apiTestRestartMessage)

	restartContext, cancelRestart := context.WithTimeout(
		t.Context(),
		apiTestRestartTimeout,
	)
	t.Cleanup(cancelRestart)
	require.NoError(t, integrationInfra.Restart(restartContext))

	page := listMessages(t, sessionID, 10, 0, "asc")
	require.Len(t, page.Items, 2)
	assert.Equal(t, apiTestRestartMessage, page.Items[0].Content)

	require.NoError(t, integrationInfra.WriteWorkspaceFile(
		t.Context(),
		apiTestWorkspaceRulesFile,
		[]byte(apiTestUpdatedRules),
	))
	_ = sendAPIWebSocketMessage(t, sessionID, apiTestReloadMessage)
	assert.Contains(t, integrationInfra.LastSystemPrompt(), apiTestUpdatedRules)
}

func TestAPICancelsActiveTurn(t *testing.T) {
	sessionID := createAPIWebSocketSession(t, "create cancellable session")

	hold, err := integrationInfra.HoldNextCompletion()
	require.NoError(t, err)
	t.Cleanup(hold.Release)

	connection := dialAPIWebSocket(t, sessionID)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	require.NoError(t, writeAPIWebSocketMessage(
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
	assert.Equal(t, string(server.ErrorCodeTurnCancelled), failure.Code)

	require.Eventually(t, func() bool {
		return !getSession(t, sessionID).ActiveTurn
	}, requestTimeout, 10*time.Millisecond)
}

func TestAPIQueuesMessageForActiveTurn(t *testing.T) {
	sessionID := createAPIWebSocketSession(t, apiTestQueuedSessionMessage)

	hold, err := integrationInfra.HoldNextCompletion()
	require.NoError(t, err)
	t.Cleanup(hold.Release)

	connection := dialAPIWebSocket(t, sessionID)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	require.NoError(t, writeAPIWebSocketMessage(
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

	queuedConnection := dialAPIWebSocket(t, sessionID)
	t.Cleanup(func() { require.NoError(t, queuedConnection.Close()) })
	require.NoError(t, writeAPIWebSocketMessage(
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

	page := listMessages(
		t,
		sessionID,
		apiTestQueuedPageLimit,
		apiTestQueuedPageOffset,
		"asc",
	)
	require.Len(t, page.Items, apiTestQueuedPageLimit)
	assert.Equal(t, apiTestActiveTurnMessage, page.Items[0].Content)
	assert.Equal(t, apiTestQueuedMessage, page.Items[2].Content)
	assert.Equal(t, api.MessageRoleAssistant, page.Items[1].Role)
	assert.Equal(t, api.MessageRoleAssistant, page.Items[3].Role)
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
	require.NoError(t, response.Body.Close())
}

func TestAPIReturnsDocumentedErrorEnvelopes(t *testing.T) {
	unknownSessionID := uuid.New()
	testCases := []struct {
		name       string
		method     string
		path       string
		headers    map[string]string
		wantStatus int
		wantCode   aichteeteapee.ErrorCode
	}{
		{
			name:       "missing bearer token",
			method:     http.MethodGet,
			path:       messagesPath,
			headers:    map[string]string{headerSessionID: unknownSessionID.String()},
			wantStatus: http.StatusUnauthorized,
			wantCode:   aichteeteapee.ErrorCodeUnauthorized,
		},
		{
			name:   "wrong bearer token",
			method: http.MethodGet,
			path:   messagesPath,
			headers: withHeader(
				withHeader(authenticatedHeaders(), headerAuthorization, bearerPrefix+"wrong"),
				headerSessionID,
				unknownSessionID.String(),
			),
			wantStatus: http.StatusUnauthorized,
			wantCode:   aichteeteapee.ErrorCodeUnauthorized,
		},
		{
			name:       "unknown session",
			method:     http.MethodGet,
			path:       sessionPath,
			headers:    withHeader(authenticatedHeaders(), headerSessionID, unknownSessionID.String()),
			wantStatus: http.StatusNotFound,
			wantCode:   server.ErrorCodeSessionNotFound,
		},
		{
			name:       "invalid page limit",
			method:     http.MethodGet,
			path:       messagesPath + "?limit=201",
			headers:    withHeader(authenticatedHeaders(), headerSessionID, unknownSessionID.String()),
			wantStatus: http.StatusBadRequest,
			wantCode:   aichteeteapee.ErrorCodeValidationFailed,
		},
		{
			name:       "malformed session id",
			method:     http.MethodGet,
			path:       sessionPath,
			headers:    withHeader(authenticatedHeaders(), headerSessionID, "not-a-uuid"),
			wantStatus: http.StatusBadRequest,
			wantCode:   server.ErrorCodeInvalidSessionID,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			response := apiRequest(t, tc.method, tc.path, nil, tc.headers)

			assert.Equal(t, tc.wantStatus, response.StatusCode)
			assertErrorCode(t, response, tc.wantCode)
		})
	}
}

func getSession(t *testing.T, sessionID uuid.UUID) api.Session {
	t.Helper()

	response := apiRequest(
		t,
		http.MethodGet,
		sessionPath,
		nil,
		withHeader(authenticatedHeaders(), headerSessionID, sessionID.String()),
	)
	requireAPIStatus(t, response, http.StatusOK)

	return decodeResponse[api.Session](t, response)
}

func listMessages(
	t *testing.T,
	sessionID uuid.UUID,
	limit int,
	offset int,
	order string,
) api.MessagePage {
	t.Helper()

	path := messagesPath + "?limit=" + strconv.Itoa(limit) +
		"&offset=" + strconv.Itoa(offset) + "&order=" + order
	response := apiRequest(
		t,
		http.MethodGet,
		path,
		nil,
		withHeader(authenticatedHeaders(), headerSessionID, sessionID.String()),
	)
	requireAPIStatus(t, response, http.StatusOK)

	return decodeResponse[api.MessagePage](t, response)
}

func apiRequest(
	t *testing.T,
	method string,
	path string,
	body []byte,
	headers map[string]string,
) *http.Response {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	t.Cleanup(cancel)

	request, err := http.NewRequestWithContext(
		ctx,
		method,
		integrationInfra.APIURL(path),
		bytes.NewReader(body),
	)
	require.NoError(t, err)
	for header, value := range headers {
		if body == nil && header == headerContentType {
			continue
		}

		request.Header.Set(header, value)
	}

	response, err := integrationInfra.HTTPClient().Do(request)
	require.NoError(t, err)

	return response
}

func decodeResponse[T any](t *testing.T, response *http.Response) T {
	t.Helper()

	body := readResponseBody(t, response)
	decoded := new(T)
	require.NoError(t, json.Unmarshal(body, decoded))

	return *decoded
}

func assertErrorCode(
	t *testing.T,
	response *http.Response,
	want aichteeteapee.ErrorCode,
) {
	t.Helper()

	errorResponse := decodeResponse[api.Error](t, response)
	assert.Equal(t, want, errorResponse.Code)
	assert.NotEmpty(t, errorResponse.Message)
}

func requireAPIStatus(
	t *testing.T,
	response *http.Response,
	wantStatus int,
) {
	t.Helper()

	if response.StatusCode == wantStatus {
		return
	}

	body := readResponseBody(t, response)
	logContext, cancelLogs := context.WithTimeout(
		context.Background(),
		requestTimeout,
	)
	logs, err := integrationInfra.AppLogs(logContext)
	cancelLogs()
	require.NoError(t, err)
	require.Failf(
		t,
		"unexpected API response status",
		"want=%d got=%d body=%s container_logs=%s",
		wantStatus,
		response.StatusCode,
		body,
		logs,
	)
}

func readResponseBody(t *testing.T, response *http.Response) []byte {
	t.Helper()

	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())

	return body
}

func authenticatedHeaders() map[string]string {
	return map[string]string{
		headerAuthorization: bearerPrefix + testinfra.TestAPIToken,
		headerContentType:   jsonMediaType,
	}
}

func withHeader(
	headers map[string]string,
	name string,
	value string,
) map[string]string {
	updated := mapsClone(headers)
	updated[name] = value

	return updated
}

func mapsClone(headers map[string]string) map[string]string {
	updated := make(map[string]string, len(headers)+1)
	for name, value := range headers {
		updated[name] = value
	}

	return updated
}

func apiMessageContents(messages []api.Message) []string {
	contents := make([]string, 0, len(messages))
	for _, message := range messages {
		contents = append(contents, message.Content)
	}

	return contents
}
