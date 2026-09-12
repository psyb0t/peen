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

func compactionsPath() string {
	return apiBaseURL + "/session/compactions"
}

func compactionPath(compactionID uuid.UUID) string {
	return compactionsPath() + "/" + compactionID.String()
}

func modelRunsPath() string {
	return apiBaseURL + "/session/model-runs"
}

func modelRunCallsPath(modelRunID uuid.UUID) string {
	return modelRunsPath() + "/" + modelRunID.String() + "/calls"
}

func TestServerSessionDurableReadEndpoints(t *testing.T) {
	sessionID := uuid.New()
	compactionID := uuid.New()
	modelRunID := uuid.New()

	testCases := []struct {
		name       string
		path       string
		runtime    *testRuntime
		wantStatus int
		wantCode   aichteeteapee.ErrorCode
	}{
		{
			name:       "lists compaction history",
			path:       compactionsPath() + "?limit=10&offset=5",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusOK,
		},
		{
			name:       "reads one compaction",
			path:       compactionPath(compactionID),
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusOK,
		},
		{
			name:       "lists model runs with filters",
			path:       modelRunsPath() + "?stage=turn&state=completed",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusOK,
		},
		{
			name:       "reads model provider rounds",
			path:       modelRunCallsPath(modelRunID) + "?limit=10&offset=5",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusOK,
		},
		{
			name:       "rejects a malformed compaction id",
			path:       compactionsPath() + "/not-a-uuid",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusBadRequest,
			wantCode:   aichteeteapee.ErrorCodeValidationFailed,
		},
		{
			name:       "rejects an oversized model run page",
			path:       modelRunsPath() + "?limit=201",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusBadRequest,
			wantCode:   aichteeteapee.ErrorCodeValidationFailed,
		},
		{
			name:       "rejects an unknown model stage",
			path:       modelRunsPath() + "?stage=unknown",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusBadRequest,
			wantCode:   aichteeteapee.ErrorCodeValidationFailed,
		},
		{
			name: "hides a compaction from another session",
			path: compactionPath(compactionID),
			runtime: &testRuntime{
				sessionID: sessionID,
				listErr:   commerr.ErrNotFound,
			},
			wantStatus: http.StatusNotFound,
			wantCode:   aichteeteapee.ErrorCodeNotFound,
		},
		{
			name: "hides a model run from another session",
			path: modelRunCallsPath(modelRunID),
			runtime: &testRuntime{
				sessionID: sessionID,
				listErr:   commerr.ErrNotFound,
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
				http.MethodGet,
				tc.path,
				nil,
			)
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

func TestServerSessionDurableReadEndpointsRequireBearerToken(t *testing.T) {
	sessionID := uuid.New()

	for _, path := range []string{
		compactionsPath(),
		compactionPath(uuid.New()),
		modelRunsPath(),
		modelRunCallsPath(uuid.New()),
	} {
		instance, err := New(Dependencies{
			Runtime:  &testRuntime{sessionID: sessionID},
			APIToken: testBearerToken,
		})
		require.NoError(t, err)

		request := httptest.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			path,
			nil,
		)
		request.Header.Set(headerSessionID, sessionID.String())
		recorder := httptest.NewRecorder()
		instance.testHandler.ServeHTTP(recorder, request)

		assert.Equal(t, http.StatusUnauthorized, recorder.Code)
		assertUnauthorizedEnvelope(t, recorder)
	}
}
