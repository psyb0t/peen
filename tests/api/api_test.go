//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/essessey"
	essesseysse "github.com/psyb0t/essessey/sse"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	server "github.com/psyb0t/peen/internal/pkg/http/server"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	apiBasePath          = "/v1"
	messagesPath         = apiBasePath + "/messages"
	sessionPath          = apiBasePath + "/session"
	sessionCancelPath    = sessionPath + "/cancel"
	headerAuthorization  = "Authorization"
	headerAccept         = "Accept"
	headerContentType    = "Content-Type"
	headerRequestID      = "X-Request-ID"
	headerSessionID      = "X-Session-ID"
	bearerPrefix         = "Bearer "
	jsonMediaType        = "application/json"
	eventStreamMediaType = "text/event-stream"
	integrationTimeout   = 5 * time.Minute
	requestTimeout       = 30 * time.Second

	// apiTestChatStatusEvent is the advisory progress frame. It is not one of
	// essessey's seven content-block types because it describes the stream
	// rather than the message.
	apiTestChatStatusEvent = "chat_status"
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
	expectedContents := make([]string, 0, len(requestMessages)*2)
	sessionID := uuid.Nil
	providedRequestID := uuid.New()

	for index, message := range requestMessages {
		headers := authenticatedHeaders()
		if index == 0 {
			headers[headerRequestID] = providedRequestID.String()
		} else {
			headers[headerSessionID] = sessionID.String()
		}

		response := apiRequest(
			t,
			http.MethodPost,
			messagesPath,
			messageJSON(t, message),
			headers,
		)
		requireAPIStatus(t, response, http.StatusOK)
		if index == 0 {
			assert.Equal(
				t,
				providedRequestID.String(),
				response.Header.Get(headerRequestID),
			)
		}

		responseSessionID := responseSessionID(t, response)
		if index == 0 {
			sessionID = responseSessionID
		} else {
			assert.Equal(t, sessionID, responseSessionID)
		}

		result := decodeResponse[api.MessageResponse](t, response)
		assert.NotEmpty(t, result.Message)
		expectedContents = append(expectedContents, message, result.Message)
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

func TestAPIStreamsAndPersistsTurn(t *testing.T) {
	response := apiRequest(
		t,
		http.MethodPost,
		messagesPath,
		messageJSON(t, "stream this request"),
		withHeader(authenticatedHeaders(), headerAccept, eventStreamMediaType),
	)
	requireAPIStatus(t, response, http.StatusOK)
	assert.True(
		t,
		strings.HasPrefix(response.Header.Get(headerContentType), eventStreamMediaType),
	)
	sessionID := responseSessionID(t, response)

	source := essesseysse.NewSource(response.Body)
	events := readSSEEvents(t, source)
	require.NoError(t, response.Body.Close())
	assert.Equal(
		t,
		[]essessey.EventType{
			// Advisory chat_status frames bracket the message. They carry
			// Chatz's own event names, and a client that only understands the
			// seven content-block types ignores them.
			apiTestChatStatusEvent,
			essessey.EventTypeMessageStart,
			essessey.EventTypePing,
			apiTestChatStatusEvent,
			apiTestChatStatusEvent,
			essessey.EventTypeContentBlockStart,
			essessey.EventTypeContentBlockDelta,
			essessey.EventTypeContentBlockStop,
			essessey.EventTypeMessageDelta,
			essessey.EventTypeMessageStop,
		},
		streamEventTypes(events),
	)

	page := listMessages(t, sessionID, 10, 0, "asc")
	require.Len(t, page.Items, 2)
	assert.Equal(t, "stream this request", page.Items[0].Content)
	assert.True(
		t,
		strings.HasPrefix(page.Items[1].Content, "integration completion "),
	)
}

func TestAPICancelsActiveTurn(t *testing.T) {
	initialResponse := apiRequest(
		t,
		http.MethodPost,
		messagesPath,
		messageJSON(t, "create cancellable session"),
		authenticatedHeaders(),
	)
	requireAPIStatus(t, initialResponse, http.StatusOK)
	_ = decodeResponse[api.MessageResponse](t, initialResponse)
	sessionID := responseSessionID(t, initialResponse)

	hold, err := integrationInfra.HoldNextCompletion()
	require.NoError(t, err)
	t.Cleanup(hold.Release)

	blockedRequest := messageJSON(t, "block this request")
	responseDone := make(chan apiCallResult, 1)
	go func() {
		responseDone <- sendMessageRequest(sessionID, blockedRequest)
	}()

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

	select {
	case result := <-responseDone:
		require.NoError(t, result.err)
		require.NotNil(t, result.response)
		requireAPIStatus(t, result.response, http.StatusConflict)
		assertErrorCode(t, result.response, server.ErrorCodeTurnCancelled)
	case <-time.After(requestTimeout):
		t.Fatal("cancelled message request did not finish")
	}

	require.Eventually(t, func() bool {
		return !getSession(t, sessionID).ActiveTurn
	}, requestTimeout, 10*time.Millisecond)
}

func TestAPIReturnsDocumentedErrorEnvelopes(t *testing.T) {
	unknownSessionID := uuid.New()
	testCases := []struct {
		name       string
		method     string
		path       string
		body       []byte
		headers    map[string]string
		wantStatus int
		wantCode   aichteeteapee.ErrorCode
	}{
		{
			name:       "missing bearer token",
			method:     http.MethodPost,
			path:       messagesPath,
			body:       messageJSON(t, "authenticate this"),
			headers:    map[string]string{headerContentType: jsonMediaType},
			wantStatus: http.StatusUnauthorized,
			wantCode:   aichteeteapee.ErrorCodeUnauthorized,
		},
		{
			name:       "wrong bearer token",
			method:     http.MethodPost,
			path:       messagesPath,
			body:       messageJSON(t, "authenticate this"),
			headers:    withHeader(authenticatedHeaders(), headerAuthorization, bearerPrefix+"wrong"),
			wantStatus: http.StatusUnauthorized,
			wantCode:   aichteeteapee.ErrorCodeUnauthorized,
		},
		{
			name:       "empty message",
			method:     http.MethodPost,
			path:       messagesPath,
			body:       []byte(`{"message":""}`),
			headers:    authenticatedHeaders(),
			wantStatus: http.StatusBadRequest,
			wantCode:   aichteeteapee.ErrorCodeValidationFailed,
		},
		{
			name:       "unsupported response representation",
			method:     http.MethodPost,
			path:       messagesPath,
			body:       messageJSON(t, "plain text is unsupported"),
			headers:    withHeader(authenticatedHeaders(), headerAccept, "text/plain"),
			wantStatus: http.StatusNotAcceptable,
			wantCode:   aichteeteapee.ErrorCodeBadRequest,
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
			response := apiRequest(t, tc.method, tc.path, tc.body, tc.headers)

			assert.Equal(t, tc.wantStatus, response.StatusCode)
			assertErrorCode(t, response, tc.wantCode)
		})
	}
}

type apiCallResult struct {
	response *http.Response
	err      error
}

func sendMessageRequest(sessionID uuid.UUID, body []byte) apiCallResult {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		integrationInfra.APIURL(messagesPath),
		bytes.NewReader(body),
	)
	if err != nil {
		return apiCallResult{err: err}
	}
	for header, value := range withHeader(
		authenticatedHeaders(),
		headerSessionID,
		sessionID.String(),
	) {
		request.Header.Set(header, value)
	}

	response, err := integrationInfra.HTTPClient().Do(request)
	if err != nil {
		return apiCallResult{err: err}
	}

	return apiCallResult{response: response}
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
		request.Header.Set(header, value)
	}

	response, err := integrationInfra.HTTPClient().Do(request)
	require.NoError(t, err)

	return response
}

func responseSessionID(t *testing.T, response *http.Response) uuid.UUID {
	t.Helper()

	sessionID, err := uuid.Parse(response.Header.Get(headerSessionID))
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, sessionID)

	return sessionID
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

func messageJSON(t *testing.T, message string) []byte {
	t.Helper()

	encoded, err := json.Marshal(api.MessageRequest{Message: message})
	require.NoError(t, err)

	return encoded
}

func apiMessageContents(messages []api.Message) []string {
	contents := make([]string, 0, len(messages))
	for _, message := range messages {
		contents = append(contents, message.Content)
	}

	return contents
}

func readSSEEvents(t *testing.T, source essessey.Source) []essessey.Event {
	t.Helper()

	events := make([]essessey.Event, 0)
	for {
		event, err := source.Next(context.Background())
		if errors.Is(err, essessey.ErrNoMoreEvents) {
			return events
		}

		require.NoError(t, err)
		events = append(events, event)
	}
}

func streamEventTypes(events []essessey.Event) []essessey.EventType {
	types := make([]essessey.EventType, 0, len(events))
	for _, event := range events {
		types = append(types, event.Event)
	}

	return types
}
