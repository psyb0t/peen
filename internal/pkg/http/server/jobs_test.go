package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validSignalBody = `{"signal":"stop"}`

func jobsPath() string {
	return apiBaseURL + "/session/jobs"
}

func jobOutputPath(jobID uuid.UUID) string {
	return apiBaseURL + "/session/jobs/" + jobID.String() + "/output"
}

func jobSignalPath(jobID uuid.UUID) string {
	return apiBaseURL + "/session/jobs/" + jobID.String() + "/signal"
}

func jobSignalsPath(jobID uuid.UUID) string {
	return apiBaseURL + "/session/jobs/" + jobID.String() + "/signals"
}

func TestServerSessionJobEndpoints(t *testing.T) {
	sessionID := uuid.New()
	jobID := uuid.New()

	testCases := []struct {
		name       string
		method     string
		path       string
		body       string
		runtime    *testRuntime
		wantStatus int
		wantCode   aichteeteapee.ErrorCode
	}{
		{
			name:       "lists jobs",
			method:     http.MethodGet,
			path:       jobsPath(),
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusOK,
		},
		{
			name:       "lists jobs filtered by state",
			method:     http.MethodGet,
			path:       jobsPath() + "?state=running",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusOK,
		},
		{
			name:       "rejects an unknown state filter",
			method:     http.MethodGet,
			path:       jobsPath() + "?state=exploded",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "lists jobs with limit and offset",
			method:     http.MethodGet,
			path:       jobsPath() + "?limit=10&offset=5",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusOK,
		},
		{
			name:       "rejects a limit over the maximum",
			method:     http.MethodGet,
			path:       jobsPath() + "?limit=500",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "rejects a negative offset",
			method:     http.MethodGet,
			path:       jobsPath() + "?offset=-1",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:   "lists jobs for an unknown session",
			method: http.MethodGet,
			path:   jobsPath(),
			runtime: &testRuntime{
				sessionID: sessionID,
				jobsErr:   commerr.ErrNotFound,
			},
			wantStatus: http.StatusNotFound,
			wantCode:   aichteeteapee.ErrorCodeNotFound,
		},
		{
			name:       "reads job output",
			method:     http.MethodGet,
			path:       jobOutputPath(jobID),
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusOK,
		},
		{
			name:       "reads job output with a cursor",
			method:     http.MethodGet,
			path:       jobOutputPath(jobID) + "?cursor=12&stream=stdout",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusOK,
		},
		{
			name:       "reads durable job signal history",
			method:     http.MethodGet,
			path:       jobSignalsPath(jobID) + "?limit=10&offset=5",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusOK,
		},
		{
			name:       "rejects an unknown stream",
			method:     http.MethodGet,
			path:       jobOutputPath(jobID) + "?stream=sideways",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "rejects a malformed job id",
			method:     http.MethodGet,
			path:       apiBaseURL + "/session/jobs/not-a-uuid/output",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:   "reads an unknown job",
			method: http.MethodGet,
			path:   jobOutputPath(jobID),
			runtime: &testRuntime{
				sessionID: sessionID,
				jobsErr:   commerr.ErrNotFound,
			},
			wantStatus: http.StatusNotFound,
			wantCode:   aichteeteapee.ErrorCodeNotFound,
		},
		{
			name:   "reads signal history for an unknown job",
			method: http.MethodGet,
			path:   jobSignalsPath(jobID),
			runtime: &testRuntime{
				sessionID: sessionID,
				jobsErr:   commerr.ErrNotFound,
			},
			wantStatus: http.StatusNotFound,
			wantCode:   aichteeteapee.ErrorCodeNotFound,
		},
		{
			name:       "signals a job",
			method:     http.MethodPost,
			path:       jobSignalPath(jobID),
			body:       validSignalBody,
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusAccepted,
		},
		{
			name:       "rejects an empty signal body",
			method:     http.MethodPost,
			path:       jobSignalPath(jobID),
			body:       "",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusBadRequest,
			wantCode:   aichteeteapee.ErrorCodeValidationFailed,
		},
		{
			name:       "rejects an unknown signal",
			method:     http.MethodPost,
			path:       jobSignalPath(jobID),
			body:       `{"signal":"obliterate"}`,
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "rejects an unknown field",
			method:     http.MethodPost,
			path:       jobSignalPath(jobID),
			body:       `{"signal":"stop","nope":1}`,
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusBadRequest,
			wantCode:   aichteeteapee.ErrorCodeValidationFailed,
		},
		{
			name:   "signals an unknown job",
			method: http.MethodPost,
			path:   jobSignalPath(jobID),
			body:   validSignalBody,
			runtime: &testRuntime{
				sessionID: sessionID,
				jobsErr:   commerr.ErrNotFound,
			},
			wantStatus: http.StatusNotFound,
			wantCode:   aichteeteapee.ErrorCodeNotFound,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			instance, err := New(Dependencies{Runtime: tc.runtime})
			require.NoError(t, err)

			request := httptest.NewRequestWithContext(
				t.Context(),
				tc.method,
				tc.path,
				strings.NewReader(tc.body),
			)
			request.Header.Set(headerSessionID, sessionID.String())
			request.Header.Set(headerContentType, mediaTypeJSON)

			recorder := httptest.NewRecorder()
			instance.testHandler.ServeHTTP(recorder, request)

			assert.Equal(t, tc.wantStatus, recorder.Code)

			if tc.wantCode != "" {
				assertErrorCode(t, recorder, tc.wantCode)
			}
		})
	}
}

// Signalling a job kills a process, so these routes must sit behind the same
// bearer check as everything else. Auth is server-wide middleware, and this
// proves the new routes did not land outside it.
func TestServerSessionJobEndpointsRequireTheBearerToken(t *testing.T) {
	sessionID := uuid.New()
	jobID := uuid.New()

	testCases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "list", method: http.MethodGet, path: jobsPath()},
		{
			name:   "read output",
			method: http.MethodGet,
			path:   jobOutputPath(jobID),
		},
		{
			name:   "signal",
			method: http.MethodPost,
			path:   jobSignalPath(jobID),
			body:   validSignalBody,
		},
		{
			name:   "list signal history",
			method: http.MethodGet,
			path:   jobSignalsPath(jobID),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			instance, err := New(Dependencies{
				Runtime:  &testRuntime{sessionID: sessionID},
				APIToken: testBearerToken,
			})
			require.NoError(t, err)

			request := httptest.NewRequestWithContext(
				t.Context(),
				tc.method,
				tc.path,
				strings.NewReader(tc.body),
			)
			request.Header.Set(headerSessionID, sessionID.String())
			request.Header.Set(headerContentType, mediaTypeJSON)

			recorder := httptest.NewRecorder()
			instance.testHandler.ServeHTTP(recorder, request)

			assert.Equal(t, http.StatusUnauthorized, recorder.Code)
			assertUnauthorizedEnvelope(t, recorder)
		})
	}
}

func TestServerSessionJobEndpointsRejectAMissingSessionHeader(t *testing.T) {
	instance, err := New(Dependencies{Runtime: &testRuntime{}})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, jobsPath(), nil)
	recorder := httptest.NewRecorder()

	instance.testHandler.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
}
