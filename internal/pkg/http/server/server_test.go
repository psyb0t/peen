package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAPIToken = "test-api-token"

func TestServerBuildsItsSerbewrListener(t *testing.T) {
	instance, err := newTestServer(Dependencies{Runtime: newTestRuntime(uuid.New())})
	require.NoError(t, err)

	assert.NotNil(t, instance.httpServer)
}

func TestServerDoesNotServeHTTPMessageSubmission(t *testing.T) {
	sessionID := uuid.New()
	runtime := newTestRuntime(sessionID)
	instance, err := newTestServer(Dependencies{
		Runtime:  runtime,
		APIToken: testAPIToken,
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		apiBaseURL+"/messages",
		nil,
	)
	request.Header.Set(headerAuthorization, bearerScheme+" "+testAPIToken)

	recorder := httptest.NewRecorder()

	instance.testHandler.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusMethodNotAllowed, recorder.Code)
	assert.Zero(t, runtime.runCalls)
}

func assertUnauthorizedEnvelope(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()

	response := api.Error{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, aichteeteapee.ErrorCodeUnauthorized, response.Code)
}

func TestServerSessionEndpoints(t *testing.T) {
	sessionID := uuid.New()
	testCases := []struct {
		name       string
		method     string
		path       string
		runtime    *testRuntime
		wantStatus int
		wantCode   aichteeteapee.ErrorCode
	}{
		{
			name:       "lists a page",
			method:     http.MethodGet,
			path:       apiBaseURL + "/messages?limit=1&offset=0&order=asc",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusOK,
		},
		{
			name:   "lists missing session",
			method: http.MethodGet,
			path:   apiBaseURL + "/messages",
			runtime: &testRuntime{
				sessionID: sessionID,
				listErr:   commerr.ErrNotFound,
			},
			wantStatus: http.StatusNotFound,
			wantCode:   ErrorCodeSessionNotFound,
		},
		{
			name:   "rejects an invalid page",
			method: http.MethodGet,
			path:   apiBaseURL + "/messages?limit=201",
			runtime: &testRuntime{
				sessionID: sessionID,
				listErr:   commerr.ErrValidationFailed,
			},
			wantStatus: http.StatusBadRequest,
			wantCode:   aichteeteapee.ErrorCodeValidationFailed,
		},
		{
			name:       "gets session details",
			method:     http.MethodGet,
			path:       apiBaseURL + "/session",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusOK,
		},
		{
			name:   "gets missing session",
			method: http.MethodGet,
			path:   apiBaseURL + "/session",
			runtime: &testRuntime{
				sessionID:  sessionID,
				sessionErr: commerr.ErrNotFound,
			},
			wantStatus: http.StatusNotFound,
			wantCode:   ErrorCodeSessionNotFound,
		},
		{
			name:       "requests cancellation",
			method:     http.MethodPost,
			path:       apiBaseURL + "/session/cancel",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusAccepted,
		},
		{
			name:   "cancels missing session",
			method: http.MethodPost,
			path:   apiBaseURL + "/session/cancel",
			runtime: &testRuntime{
				sessionID: sessionID,
				cancelErr: commerr.ErrNotFound,
			},
			wantStatus: http.StatusNotFound,
			wantCode:   ErrorCodeSessionNotFound,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			instance, err := newTestServer(Dependencies{Runtime: tc.runtime})
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

// TestServerCancelReachesTheSessionWorker proves the cancel endpoint reaches
// the process that runs the model loop.
//
// The controller can mark a turn cancelled in SQLite without stopping
// anything, because the turn runs in the session's worker. An endpoint that
// only wrote the durable flag answered cancelRequested while the turn ran to
// completion, which is the one control an operator has over a runaway turn.
func TestServerCancelReachesTheSessionWorker(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	runtime := newTestRuntime(sessionID)
	router := &testTurnRouter{runtime: runtime, cancelRouted: true}

	instance, err := newTestServer(Dependencies{
		Runtime: runtime,
		Turns:   router,
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		apiBaseURL+"/session/cancel",
		nil,
	)
	request.Header.Set(headerSessionID, sessionID.String())

	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusAccepted, recorder.Code)
	assert.Equal(t, []uuid.UUID{sessionID}, router.cancelledSessions)

	response := api.CancelResponse{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))

	// The stub runtime reports no durable turn, so a true answer here can only
	// come from the worker the request was routed to.
	assert.True(t, response.CancelRequested)
}

// TestServerCancelReportsAnIdleSessionHonestly keeps the routed call from
// inventing a cancellation when no worker had a turn to stop.
func TestServerCancelReportsAnIdleSessionHonestly(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	runtime := newTestRuntime(sessionID)
	router := &testTurnRouter{runtime: runtime, cancelRouted: false}

	instance, err := newTestServer(Dependencies{
		Runtime: runtime,
		Turns:   router,
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		apiBaseURL+"/session/cancel",
		nil,
	)
	request.Header.Set(headerSessionID, sessionID.String())

	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusAccepted, recorder.Code)
	assert.Equal(t, []uuid.UUID{sessionID}, router.cancelledSessions)

	response := api.CancelResponse{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.False(t, response.CancelRequested)
}

// TestServerCancelSurfacesAWorkerFailure keeps a broken worker route from
// being reported to the operator as an accepted cancellation.
func TestServerCancelSurfacesAWorkerFailure(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	runtime := newTestRuntime(sessionID)
	router := &testTurnRouter{
		runtime:   runtime,
		cancelErr: commerr.ErrFetchFailed,
	}

	instance, err := newTestServer(Dependencies{
		Runtime: runtime,
		Turns:   router,
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		apiBaseURL+"/session/cancel",
		nil,
	)
	request.Header.Set(headerSessionID, sessionID.String())

	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
}

// TestServerJobSignalReachesTheSessionWorker proves the signal endpoint
// reaches the process group it is meant to stop.
//
// A job runs in the session's worker, so the controller's own job registry is
// empty. An endpoint that consulted only that registry recorded every signal
// as unaccepted and reported it was not signalled, while the command kept
// running with no way to stop it.
func TestServerJobSignalReachesTheSessionWorker(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	jobID := uuid.New()
	runtime := newTestRuntime(sessionID)
	router := &testTurnRouter{
		runtime: runtime,
		signalResponse: &api.JobSignalResponse{
			Signalled: true,
			State:     api.JobSignalResponseStateSignalled,
		},
	}

	instance, err := newTestServer(Dependencies{
		Runtime: runtime,
		Turns:   router,
	})
	require.NoError(t, err)

	recorder := signalTestJob(t, instance, sessionID, jobID)
	require.Equal(t, http.StatusAccepted, recorder.Code)
	assert.Equal(t, []uuid.UUID{jobID}, router.signalledJobs)
	assert.Equal(t, string(api.JobSignalRequestSignalStop), router.signalSignal)

	response := api.JobSignalResponse{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.True(t, response.Signalled)
	assert.Equal(t, jobID, response.JobId)
}

// TestServerJobSignalFallsBackWithoutALiveWorker keeps a session whose worker
// has gone answering honestly instead of failing. The request is still
// recorded against the durable row.
func TestServerJobSignalFallsBackWithoutALiveWorker(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	jobID := uuid.New()
	runtime := newTestRuntime(sessionID)
	router := &testTurnRouter{runtime: runtime}

	instance, err := newTestServer(Dependencies{
		Runtime: runtime,
		Turns:   router,
	})
	require.NoError(t, err)

	recorder := signalTestJob(t, instance, sessionID, jobID)
	require.Equal(t, http.StatusAccepted, recorder.Code)
	assert.Equal(t, []uuid.UUID{jobID}, router.signalledJobs)

	response := api.JobSignalResponse{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, jobID, response.JobId)

	// The two paths report different states, so this is what says the durable
	// fallback answered rather than a worker.
	assert.Equal(t, api.JobSignalResponseStateRunning, response.State)
}

// TestServerJobSignalSurfacesAWorkerFailure keeps a broken worker route from
// being reported to the operator as an accepted signal.
func TestServerJobSignalSurfacesAWorkerFailure(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	runtime := newTestRuntime(sessionID)
	router := &testTurnRouter{
		runtime:   runtime,
		signalErr: commerr.ErrFetchFailed,
	}

	instance, err := newTestServer(Dependencies{
		Runtime: runtime,
		Turns:   router,
	})
	require.NoError(t, err)

	recorder := signalTestJob(t, instance, sessionID, uuid.New())
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
}

func signalTestJob(
	t *testing.T,
	instance *Server,
	sessionID, jobID uuid.UUID,
) *httptest.ResponseRecorder {
	t.Helper()

	payload, err := json.Marshal(api.JobSignalRequest{
		Signal: api.JobSignalRequestSignalStop,
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		apiBaseURL+"/session/jobs/"+jobID.String()+"/signal",
		bytes.NewReader(payload),
	)
	request.Header.Set(headerSessionID, sessionID.String())
	request.Header.Set(headerContentType, string(mediaTypeJSON))

	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(recorder, request)

	return recorder
}

// A malformed X-Session-ID header fails oapi-codegen's own UUID parsing
// before the request reaches a handler, distinct from a body shape
// rejection, so it carries its own documented code.
func TestServerRejectsMalformedSessionIDHeader(t *testing.T) {
	instance, err := newTestServer(Dependencies{
		Runtime: newTestRuntime(uuid.New()),
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		apiBaseURL+"/session",
		nil,
	)
	request.Header.Set(headerSessionID, "not-a-uuid")

	recorder := httptest.NewRecorder()
	instance.testHandler.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assertErrorCode(t, recorder, ErrorCodeInvalidSessionID)
}

func assertErrorCode(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
	want aichteeteapee.ErrorCode,
) {
	t.Helper()

	response := api.Error{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, want, response.Code)
}

type testRuntime struct {
	sessionID  uuid.UUID
	modelList  api.ModelList
	runCalls   int
	runErr     error
	runQueued  bool
	runEvent   agent.Event
	sessionErr error
	listErr    error
	cancelErr  error
	eventsErr  error
	jobsErr    error

	agentRunsErr error

	// lastRequest records what the transport handed the runtime, so a routing
	// test can assert which session a message was sent to.
	lastRequest agent.MessageRequest
}

func newTestRuntime(sessionID uuid.UUID) *testRuntime {
	return &testRuntime{sessionID: sessionID}
}

func (r *testRuntime) SessionID() uuid.UUID {
	return r.sessionID
}

func (r *testRuntime) ListModels() api.ModelList {
	return r.modelList
}

func (r *testRuntime) RunMessage(
	_ context.Context,
	request agent.MessageRequest,
	_ uuid.UUID,
	sink agent.EventSink,
) (*agent.MessageRunResult, error) {
	r.runCalls++

	r.lastRequest = request
	if r.runErr != nil {
		return nil, r.runErr
	}

	if sink != nil && r.runEvent.Type != "" {
		if err := sink(r.runEvent); err != nil {
			return nil, ctxerrors.Wrap(err, "send test agent event")
		}
	}

	return &agent.MessageRunResult{
		SessionID: r.sessionID,
		Queued:    r.runQueued,
		Text:      "response",
	}, nil
}

func (r *testRuntime) Session(
	_ context.Context,
	_ uuid.UUID,
) (*api.Session, error) {
	if r.sessionErr != nil {
		return nil, r.sessionErr
	}

	return &api.Session{Id: r.sessionID}, nil
}

func (r *testRuntime) ListMessages(
	_ context.Context,
	_ api.ListMessagesParams,
) (*api.MessagePage, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}

	return &api.MessagePage{}, nil
}

func (r *testRuntime) CancelSession(
	_ context.Context,
	_ uuid.UUID,
) (*api.CancelResponse, error) {
	if r.cancelErr != nil {
		return nil, r.cancelErr
	}

	return &api.CancelResponse{}, nil
}

func (r *testRuntime) ListSessionEvents(
	_ context.Context,
	_ api.ListSessionEventsParams,
) (*api.TranscriptEventPage, error) {
	if r.eventsErr != nil {
		return nil, r.eventsErr
	}

	return &api.TranscriptEventPage{Events: []api.TranscriptEvent{}}, nil
}

func (r *testRuntime) ListSessionNotices(
	_ context.Context,
	_ api.ListSessionNoticesParams,
) (*api.SessionNoticePage, error) {
	if r.eventsErr != nil {
		return nil, r.eventsErr
	}

	return &api.SessionNoticePage{Notices: []api.SessionNotice{}}, nil
}

func (r *testRuntime) PublishSessionNotice(
	_ context.Context,
	_ uuid.UUID,
	request api.SessionNoticeRequest,
) (*api.SessionNotice, error) {
	if r.eventsErr != nil {
		return nil, r.eventsErr
	}

	return &api.SessionNotice{
		Id:       uuid.New(),
		Type:     request.Type,
		Source:   "api",
		Summary:  request.Summary,
		Delivery: api.SessionNoticeDeliveryQueue,
	}, nil
}

func (r *testRuntime) ListSessionTurns(
	_ context.Context,
	_ api.ListSessionTurnsParams,
) (*api.TurnPage, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}

	return &api.TurnPage{Turns: []api.Turn{}}, nil
}

func (r *testRuntime) ListSessionCompactions(
	_ context.Context,
	_ api.ListSessionCompactionsParams,
) (*api.CompactionPage, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}

	return &api.CompactionPage{Compactions: []api.Compaction{}}, nil
}

func (r *testRuntime) GetSessionCompaction(
	_ context.Context,
	_ uuid.UUID,
	compactionID uuid.UUID,
) (*api.Compaction, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}

	return &api.Compaction{Id: compactionID}, nil
}

func (r *testRuntime) ListSessionModelRuns(
	_ context.Context,
	_ api.ListSessionModelRunsParams,
) (*api.ModelRunPage, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}

	return &api.ModelRunPage{ModelRuns: []api.ModelRun{}}, nil
}

func (r *testRuntime) ListSessionModelRunCalls(
	_ context.Context,
	_ uuid.UUID,
	modelRunID uuid.UUID,
	_ api.ListSessionModelRunCallsParams,
) (*api.ModelCallPage, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}

	return &api.ModelCallPage{Calls: []api.ModelCall{}, ModelRun: api.ModelRun{Id: modelRunID}}, nil
}

func (r *testRuntime) GetSessionContextSnapshot(
	_ context.Context,
	_ uuid.UUID,
	_ string,
) (*api.ContextSnapshot, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}

	return &api.ContextSnapshot{Manifest: []api.ContextManifestEntry{}}, nil
}

func (r *testRuntime) GetSessionPromptSnapshot(
	_ context.Context,
	_ uuid.UUID,
	_ string,
) (*api.PromptSnapshot, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}

	return &api.PromptSnapshot{}, nil
}

func (r *testRuntime) ListSessionJobs(
	_ context.Context,
	_ uuid.UUID,
	_ api.ListSessionJobsParams,
) (*api.JobPage, error) {
	if r.jobsErr != nil {
		return nil, r.jobsErr
	}

	return &api.JobPage{Jobs: []api.Job{}}, nil
}

func (r *testRuntime) ReadSessionJobOutput(
	_ context.Context,
	_ uuid.UUID,
	jobID uuid.UUID,
	_ api.ReadSessionJobOutputParams,
) (*api.JobOutput, error) {
	if r.jobsErr != nil {
		return nil, r.jobsErr
	}

	return &api.JobOutput{
		JobId: jobID,
		State: api.JobOutputStateRunning,
	}, nil
}

func (r *testRuntime) ListSessionJobSignalRequests(
	_ context.Context,
	_ uuid.UUID,
	jobID uuid.UUID,
	_ api.ListSessionJobSignalRequestsParams,
) (*api.JobSignalRecordPage, error) {
	if r.jobsErr != nil {
		return nil, r.jobsErr
	}

	return &api.JobSignalRecordPage{
		Job:            api.Job{JobId: jobID},
		SignalRequests: []api.JobSignalRecord{},
	}, nil
}

func (r *testRuntime) SignalSessionJob(
	_ context.Context,
	_ uuid.UUID,
	jobID uuid.UUID,
	_ api.JobSignalRequest,
) (*api.JobSignalResponse, error) {
	if r.jobsErr != nil {
		return nil, r.jobsErr
	}

	return &api.JobSignalResponse{
		JobId:     jobID,
		Signalled: true,
		State:     api.JobSignalResponseStateRunning,
	}, nil
}

func (r *testRuntime) ListSessionAgentRuns(
	_ context.Context,
	_ uuid.UUID,
	_ api.ListSessionAgentRunsParams,
) (*api.AgentRunPage, error) {
	if r.agentRunsErr != nil {
		return nil, r.agentRunsErr
	}

	return &api.AgentRunPage{Agents: []api.AgentRun{}}, nil
}

func (r *testRuntime) ListSessionAgentRunEvents(
	_ context.Context,
	_ uuid.UUID,
	agentRunID uuid.UUID,
	_ api.ListSessionAgentRunEventsParams,
) (*api.AgentRunEventPage, error) {
	if r.agentRunsErr != nil {
		return nil, r.agentRunsErr
	}

	return &api.AgentRunEventPage{
		AgentRunId: agentRunID,
		State:      api.AgentRunEventPageStateRunning,
		Events:     []api.AgentRunEvent{},
	}, nil
}

func (r *testRuntime) GetSessionAgentRun(
	_ context.Context,
	_ uuid.UUID,
	agentRunID uuid.UUID,
) (*api.AgentRun, error) {
	if r.agentRunsErr != nil {
		return nil, r.agentRunsErr
	}

	return &api.AgentRun{AgentRunId: agentRunID}, nil
}

// A child agent's transcript and compaction lineage are separate durable
// records, so the double answers them from their own error seam.
func (r *testRuntime) ListSessionAgentRunMessages(
	_ context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	_ api.ListSessionAgentRunMessagesParams,
) (*api.AgentRunMessagePage, error) {
	if r.agentRunsErr != nil {
		return nil, r.agentRunsErr
	}

	return &api.AgentRunMessagePage{
		Messages: []api.AgentRunMessage{
			{AgentRunId: agentRunID, SessionId: sessionID, Sequence: 1},
		},
	}, nil
}

func (r *testRuntime) ListSessionAgentRunCompactions(
	_ context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	_ api.ListSessionAgentRunCompactionsParams,
) (*api.AgentRunCompactionPage, error) {
	if r.agentRunsErr != nil {
		return nil, r.agentRunsErr
	}

	return &api.AgentRunCompactionPage{
		Compactions: []api.AgentRunCompaction{
			{AgentRunId: agentRunID, SessionId: sessionID},
		},
	}, nil
}

func (r *testRuntime) GetSessionAgentRunCompaction(
	_ context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	compactionID uuid.UUID,
) (*api.AgentRunCompaction, error) {
	if r.agentRunsErr != nil {
		return nil, r.agentRunsErr
	}

	return &api.AgentRunCompaction{
		Id:         compactionID,
		AgentRunId: agentRunID,
		SessionId:  sessionID,
	}, nil
}

func (r *testRuntime) CancelSessionAgentRun(
	_ context.Context,
	_ uuid.UUID,
	agentRunID uuid.UUID,
) (*api.AgentRunCancelResponse, error) {
	if r.agentRunsErr != nil {
		return nil, r.agentRunsErr
	}

	return &api.AgentRunCancelResponse{
		AgentRunId:      agentRunID,
		CancelRequested: true,
		State:           api.AgentRunCancelResponseStateRunning,
	}, nil
}

var _ agent.API = (*testRuntime)(nil)
