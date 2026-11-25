//go:build integration

package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	server "github.com/psyb0t/peen/internal/pkg/http/server"
	"github.com/stretchr/testify/assert"
)

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

type recordRequest struct {
	name        string
	method      string
	path        string
	body        string
	wantMessage string
}

// recordRequests are the operations that name a record inside the
// X-Session-ID session. Every record ID is random, so none exists.
func recordRequests() []recordRequest {
	recordID := uuid.NewString()
	agentRun := agentRunsPath + "/" + uuid.NewString()
	hash := strings.Repeat("b", 64)
	learned := learnedTestFilePath + "?scope=workspace&kind=rule&name=absent"

	return []recordRequest{
		{
			"job output", http.MethodGet,
			sessionPath + "/jobs/" + recordID + "/output", "", "job not found",
		},
		{
			"job signals", http.MethodGet,
			sessionPath + "/jobs/" + recordID + "/signals", "", "job not found",
		},
		{
			"job signal", http.MethodPost,
			sessionPath + "/jobs/" + recordID + "/signal", `{"signal":"stop"}`,
			"job not found",
		},
		{
			"compaction", http.MethodGet,
			sessionPath + "/compactions/" + recordID, "",
			"compaction not found",
		},
		{
			"model run calls", http.MethodGet,
			modelRunsPath + "/" + recordID + "/calls", "",
			"model run not found",
		},
		{
			"context snapshot", http.MethodGet,
			sessionPath + "/context-snapshots/" + hash, "",
			"context snapshot not found",
		},
		{
			"prompt snapshot", http.MethodGet,
			sessionPath + "/prompt-snapshots/" + hash, "",
			"prompt snapshot not found",
		},
		{"agent run", http.MethodGet, agentRun, "", "agent run not found"},
		{
			"agent run events", http.MethodGet, agentRun + "/events", "",
			"agent run not found",
		},
		{
			"agent run messages", http.MethodGet, agentRun + "/messages", "",
			"agent run not found",
		},
		{
			"agent run compactions", http.MethodGet, agentRun + "/compactions",
			"", "agent run not found",
		},
		{
			"agent run compaction", http.MethodGet,
			agentRun + "/compactions/" + recordID, "", "compaction not found",
		},
		{
			"agent run cancel", http.MethodPost, agentRun + "/cancel", "",
			"agent run not found",
		},
		{"learned file", http.MethodGet, learned, "", "learned file not found"},
		{
			"learned file delete", http.MethodDelete, learned, "",
			"learned file not found",
		},
		{
			"workspace listing", http.MethodGet, fileAPIPath + "?path=absent",
			"", "workspace path not found",
		},
		{
			"workspace read", http.MethodGet,
			fileAPIPath + "/content?path=absent", "",
			"workspace path not found",
		},
		{
			"workspace delete", http.MethodDelete, fileAPIPath + "?path=absent",
			"", "workspace path not found",
		},
	}
}

// Every operation that takes X-Session-ID answers an unknown session with
// 404 SESSION_NOT_FOUND, whatever else the request names.
func TestAPIUnknownSessionIsSessionNotFoundEverywhere(t *testing.T) {
	headers := withHeader(authenticatedHeaders(), headerSessionID,
		uuid.NewString())

	sessionOnly := []recordRequest{
		{name: "events", method: http.MethodGet, path: sessionPath + "/events"},
		{name: "notices", method: http.MethodGet, path: sessionNoticesPath},
		{
			name: "publish notice", method: http.MethodPost,
			path: sessionNoticesPath,
			body: `{"type":"ci.build.failed","summary":"red"}`,
		},
		{name: "jobs", method: http.MethodGet, path: sessionPath + "/jobs"},
		{name: "agent runs", method: http.MethodGet, path: agentRunsPath},
		{name: "turns", method: http.MethodGet, path: sessionPath + "/turns"},
		{
			name: "learned files", method: http.MethodGet,
			path: learnedTestListPath,
		},
		{name: "mcp", method: http.MethodGet, path: mcpTestStatusPath},
		{
			name: "workspace write", method: http.MethodPut,
			path: fileAPIPath + "/content",
			body: `{"path":"absent","contentBase64":"eA=="}`,
		},
	}

	for _, request := range append(sessionOnly, recordRequests()...) {
		t.Run(request.name, func(t *testing.T) {
			var body []byte
			if request.body != "" {
				body = []byte(request.body)
			}

			response := apiRequest(t, request.method, request.path, body,
				headers)
			requireAPIStatus(t, response, http.StatusNotFound)

			envelope := decodeResponse[api.Error](t, response)
			assert.Equal(t, server.ErrorCodeSessionNotFound, envelope.Code)
			assert.Equal(t, "session not found", envelope.Message)
		})
	}
}

// A record missing inside an existing session answers 404 NOT_FOUND with a
// message naming what is missing, never "session not found".
func TestAPIMissingRecordIsNotFoundNamingIt(t *testing.T) {
	workspace := openFreshWorkspace(t, "missing-records-", nil)
	headers := withHeader(authenticatedHeaders(), headerSessionID,
		workspace.sessionID.String())

	for _, request := range recordRequests() {
		t.Run(request.name, func(t *testing.T) {
			var body []byte
			if request.body != "" {
				body = []byte(request.body)
			}

			response := apiRequest(t, request.method, request.path, body,
				headers)
			requireAPIStatus(t, response, http.StatusNotFound)

			envelope := decodeResponse[api.Error](t, response)
			assert.Equal(t, aichteeteapee.ErrorCodeNotFound, envelope.Code)
			assert.Equal(t, request.wantMessage, envelope.Message)
		})
	}
}

// Every response is JSON, so an Accept header that rules JSON out is refused
// before the operation runs.
func TestAPIRefusesAnAcceptHeaderWithoutJSON(t *testing.T) {
	headers := withHeader(authenticatedHeaders(), headerAccept, "text/html")
	headers = withHeader(headers, headerSessionID, uuid.NewString())

	for _, path := range []string{sessionPath, agentSpacesPath} {
		response := apiRequest(t, http.MethodGet, path, nil, headers)
		requireAPIStatus(t, response, http.StatusNotAcceptable)

		envelope := decodeResponse[api.Error](t, response)
		assert.Equal(t, aichteeteapee.ErrorCodeBadRequest, envelope.Code)
		assert.Equal(t, "unsupported response representation",
			envelope.Message)
	}
}
