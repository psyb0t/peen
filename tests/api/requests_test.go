//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func listModelRuns(
	t *testing.T,
	sessionID uuid.UUID,
	limit int,
	offset int,
) api.ModelRunPage {
	t.Helper()

	path := modelRunsPath + "?limit=" + strconv.Itoa(limit) +
		"&offset=" + strconv.Itoa(offset)
	response := apiRequest(
		t,
		http.MethodGet,
		path,
		nil,
		withHeader(authenticatedHeaders(), headerSessionID, sessionID.String()),
	)
	requireAPIStatus(t, response, http.StatusOK)

	return decodeResponse[api.ModelRunPage](t, response)
}

func listModelRunCalls(
	t *testing.T,
	sessionID uuid.UUID,
	modelRunID uuid.UUID,
	limit int,
	offset int,
) api.ModelCallPage {
	t.Helper()

	path := modelRunsPath + "/" + modelRunID.String() + "/calls?limit=" +
		strconv.Itoa(limit) + "&offset=" + strconv.Itoa(offset)
	response := apiRequest(
		t,
		http.MethodGet,
		path,
		nil,
		withHeader(authenticatedHeaders(), headerSessionID, sessionID.String()),
	)
	requireAPIStatus(t, response, http.StatusOK)

	return decodeResponse[api.ModelCallPage](t, response)
}

func apiRequest(
	t *testing.T,
	method string,
	path string,
	body []byte,
	headers map[string]string,
) *apiResponse {
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

	defer func() { require.NoError(t, response.Body.Close()) }()

	return readAPIResponse(t, response)
}

// apiResponse is one HTTP response read to the end, so a test never holds an
// open body.
type apiResponse struct {
	StatusCode int
	Body       []byte
}

// readAPIResponse reads the whole body of response. The caller closes it.
func readAPIResponse(t *testing.T, response *http.Response) *apiResponse {
	t.Helper()

	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)

	return &apiResponse{StatusCode: response.StatusCode, Body: body}
}

//nolint:ireturn // The type parameter is the decoded JSON value, not an interface.
func decodeResponse[T any](t *testing.T, response *apiResponse) T {
	t.Helper()

	decoded := new(T)
	require.NoError(t, json.Unmarshal(response.Body, decoded))

	return *decoded
}

func assertErrorCode(
	t *testing.T,
	response *apiResponse,
	want aichteeteapee.ErrorCode,
) {
	t.Helper()

	errorResponse := decodeResponse[api.Error](t, response)
	assert.Equal(t, want, errorResponse.Code)
	assert.NotEmpty(t, errorResponse.Message)
}

func requireAPIStatus(
	t *testing.T,
	response *apiResponse,
	wantStatus int,
) {
	t.Helper()

	if response.StatusCode == wantStatus {
		return
	}

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
		response.Body,
		logs,
	)
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
