package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func agentRunsPath() string {
	return apiBaseURL + "/session/agents"
}

func agentRunEventsPath(runID uuid.UUID) string {
	return agentRunsPath() + "/" + runID.String() + "/messages"
}

func agentRunCancelPath(runID uuid.UUID) string {
	return agentRunsPath() + "/" + runID.String() + "/cancel"
}

func TestServerSessionAgentRunEndpoints(t *testing.T) {
	sessionID := uuid.New()
	runID := uuid.New()

	testCases := []struct {
		name       string
		method     string
		path       string
		runtime    *testRuntime
		wantStatus int
		wantCode   aichteeteapee.ErrorCode
	}{
		{
			name:       "lists agent runs",
			method:     http.MethodGet,
			path:       agentRunsPath(),
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusOK,
		},
		{
			name:       "lists agent runs filtered by state",
			method:     http.MethodGet,
			path:       agentRunsPath() + "?state=running",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusOK,
		},
		{
			name:       "rejects an unknown state filter",
			method:     http.MethodGet,
			path:       agentRunsPath() + "?state=ascended",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusBadRequest,
			wantCode:   aichteeteapee.ErrorCodeValidationFailed,
		},
		{
			name:       "lists agent runs with limit and offset",
			method:     http.MethodGet,
			path:       agentRunsPath() + "?limit=10&offset=5",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusOK,
		},
		{
			name:       "rejects a limit over the maximum",
			method:     http.MethodGet,
			path:       agentRunsPath() + "?limit=500",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "rejects a negative offset",
			method:     http.MethodGet,
			path:       agentRunsPath() + "?offset=-1",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:   "lists for an unknown session",
			method: http.MethodGet,
			path:   agentRunsPath(),
			runtime: &testRuntime{
				sessionID:    sessionID,
				agentRunsErr: commerr.ErrNotFound,
			},
			wantStatus: http.StatusNotFound,
			wantCode:   aichteeteapee.ErrorCodeNotFound,
		},
		{
			name:       "follows one agent run",
			method:     http.MethodGet,
			path:       agentRunEventsPath(runID),
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusOK,
		},
		{
			name:       "follows with a cursor",
			method:     http.MethodGet,
			path:       agentRunEventsPath(runID) + "?cursor=5&limit=10",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusOK,
		},
		{
			name:       "rejects a malformed agent run id",
			method:     http.MethodGet,
			path:       agentRunsPath() + "/not-a-uuid/messages",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:   "follows an unknown agent run",
			method: http.MethodGet,
			path:   agentRunEventsPath(runID),
			runtime: &testRuntime{
				sessionID:    sessionID,
				agentRunsErr: commerr.ErrNotFound,
			},
			wantStatus: http.StatusNotFound,
			wantCode:   aichteeteapee.ErrorCodeNotFound,
		},
		{
			name:       "cancels an agent run",
			method:     http.MethodPost,
			path:       agentRunCancelPath(runID),
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusAccepted,
		},
		{
			name:   "cancels an unknown agent run",
			method: http.MethodPost,
			path:   agentRunCancelPath(runID),
			runtime: &testRuntime{
				sessionID:    sessionID,
				agentRunsErr: commerr.ErrNotFound,
			},
			wantStatus: http.StatusNotFound,
			wantCode:   aichteeteapee.ErrorCodeNotFound,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			instance, err := New(Dependencies{Runtime: tc.runtime})
			require.NoError(t, err)

			request := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)
			request.Header.Set(headerSessionID, sessionID.String())

			recorder := httptest.NewRecorder()
			instance.testHandler.ServeHTTP(recorder, request)

			assert.Equal(t, tc.wantStatus, recorder.Code)

			if tc.wantCode != "" {
				assertErrorCode(t, recorder, tc.wantCode)
			}
		})
	}
}

// Cancelling a child agent stops real work, so these routes must sit behind
// the same bearer check as everything else.
func TestServerSessionAgentRunEndpointsRequireTheBearerToken(t *testing.T) {
	sessionID := uuid.New()
	runID := uuid.New()

	testCases := []struct {
		name   string
		method string
		path   string
	}{
		{name: "list", method: http.MethodGet, path: agentRunsPath()},
		{
			name:   "follow",
			method: http.MethodGet,
			path:   agentRunEventsPath(runID),
		},
		{
			name:   "cancel",
			method: http.MethodPost,
			path:   agentRunCancelPath(runID),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			instance, err := New(Dependencies{
				Runtime:  &testRuntime{sessionID: sessionID},
				APIToken: testBearerToken,
			})
			require.NoError(t, err)

			request := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)
			request.Header.Set(headerSessionID, sessionID.String())

			recorder := httptest.NewRecorder()
			instance.testHandler.ServeHTTP(recorder, request)

			assert.Equal(t, http.StatusUnauthorized, recorder.Code)
			assertUnauthorizedEnvelope(t, recorder)
		})
	}
}

func TestServerSessionAgentRunEndpointsRejectMissingSessionHeader(t *testing.T) {
	instance, err := New(Dependencies{Runtime: &testRuntime{}})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, agentRunsPath(), nil)
	recorder := httptest.NewRecorder()

	instance.testHandler.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
}
