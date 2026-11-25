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

type notFoundRoute struct {
	name        string
	method      string
	path        string
	body        string
	wantMessage string
	fail        func(runtime *testRuntime, registry *testSessionRegistry)
}

func notFoundRoutes() []notFoundRoute {
	jobID := uuid.New().String()
	agentRunID := uuid.New().String()
	recordID := uuid.New().String()
	hash := strings.Repeat("a", 64)
	learnedQuery := "?scope=workspace&kind=rule&name=go/errors"

	failJobs := func(runtime *testRuntime, _ *testSessionRegistry) {
		runtime.jobsErr = commerr.ErrNotFound
	}
	failReads := func(runtime *testRuntime, _ *testSessionRegistry) {
		runtime.listErr = commerr.ErrNotFound
	}
	failAgentRuns := func(runtime *testRuntime, _ *testSessionRegistry) {
		runtime.agentRunsErr = commerr.ErrNotFound
	}
	failLearned := func(_ *testRuntime, registry *testSessionRegistry) {
		registry.learnedErr = commerr.ErrNotFound
	}

	agentRunPrefix := "/session/agents/" + agentRunID

	return []notFoundRoute{
		{
			"job output", http.MethodGet, "/session/jobs/" + jobID + "/output",
			"", jobNotFoundMessage, failJobs,
		},
		{
			"job signals", http.MethodGet, "/session/jobs/" + jobID + "/signals",
			"", jobNotFoundMessage, failJobs,
		},
		{
			"job signal", http.MethodPost, "/session/jobs/" + jobID + "/signal",
			validSignalBody, jobNotFoundMessage, failJobs,
		},
		{
			"compaction", http.MethodGet, "/session/compactions/" + recordID,
			"", compactionNotFoundMessage, failReads,
		},
		{
			"model run calls", http.MethodGet,
			"/session/model-runs/" + recordID + "/calls",
			"", modelRunNotFoundMessage, failReads,
		},
		{
			"context snapshot", http.MethodGet,
			"/session/context-snapshots/" + hash,
			"", contextSnapshotNotFoundMessage, failReads,
		},
		{
			"prompt snapshot", http.MethodGet,
			"/session/prompt-snapshots/" + hash,
			"", promptSnapshotNotFoundMessage, failReads,
		},
		{
			"agent run", http.MethodGet, agentRunPrefix,
			"", agentRunNotFoundMessage, failAgentRuns,
		},
		{
			"agent run events", http.MethodGet, agentRunPrefix + "/events",
			"", agentRunNotFoundMessage, failAgentRuns,
		},
		{
			"agent run messages", http.MethodGet, agentRunPrefix + "/messages",
			"", agentRunNotFoundMessage, failAgentRuns,
		},
		{
			"agent run compactions", http.MethodGet,
			agentRunPrefix + "/compactions",
			"", agentRunNotFoundMessage, failAgentRuns,
		},
		{
			"agent run compaction", http.MethodGet,
			agentRunPrefix + "/compactions/" + recordID,
			"", compactionNotFoundMessage, failAgentRuns,
		},
		{
			"agent run cancel", http.MethodPost, agentRunPrefix + "/cancel",
			"", agentRunNotFoundMessage, failAgentRuns,
		},
		{
			"learned file", http.MethodGet, "/session/learned/file" + learnedQuery,
			"", learnedFileNotFoundMessage, failLearned,
		},
		{
			"learned file delete", http.MethodDelete,
			"/session/learned/file" + learnedQuery,
			"", learnedFileNotFoundMessage, failLearned,
		},
	}
}

// A record missing inside an existing session and a session that does not
// exist reach the handler as the same store error. The response must still
// tell the two apart, and name only what is missing.
func TestRecordRoutesTellAMissingSessionFromAMissingRecord(t *testing.T) {
	t.Parallel()

	for _, route := range notFoundRoutes() {
		t.Run(route.name, func(t *testing.T) {
			t.Parallel()

			t.Run("record missing", func(t *testing.T) {
				t.Parallel()

				recorder := serveNotFoundRoute(t, route, nil)
				require.Equal(t, http.StatusNotFound, recorder.Code,
					recorder.Body.String())
				assertErrorEnvelope(t, recorder,
					aichteeteapee.ErrorCodeNotFound, route.wantMessage)
			})

			t.Run("session missing", func(t *testing.T) {
				t.Parallel()

				recorder := serveNotFoundRoute(t, route, commerr.ErrNotFound)
				require.Equal(t, http.StatusNotFound, recorder.Code,
					recorder.Body.String())
				assertErrorEnvelope(t, recorder, ErrorCodeSessionNotFound,
					sessionNotFoundMessage)
			})
		})
	}
}

func TestRecordRoutesAnswer500WhenTheSessionLookupFails(t *testing.T) {
	t.Parallel()

	route := notFoundRoutes()[0]
	recorder := serveNotFoundRoute(t, route, commerr.ErrInvalidState)
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assertErrorCode(t, recorder, aichteeteapee.ErrorCodeInternalServerError)
}

func serveNotFoundRoute(
	t *testing.T,
	route notFoundRoute,
	sessionErr error,
) *httptest.ResponseRecorder {
	t.Helper()

	sessionID := uuid.New()
	runtime := newTestRuntime(sessionID)
	runtime.sessionErr = sessionErr
	registry := &testSessionRegistry{}
	route.fail(runtime, registry)

	instance, err := newTestServer(Dependencies{
		Runtime:  runtime,
		Sessions: registry,
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(t.Context(), route.method,
		apiBaseURL+route.path, strings.NewReader(route.body))
	request.Header.Set(headerSessionID, sessionID.String())

	if route.body != "" {
		request.Header.Set(headerContentType, mediaTypeJSON)
	}

	recorder := httptest.NewRecorder()
	instance.inProcessHandler.ServeHTTP(recorder, request)

	return recorder
}
