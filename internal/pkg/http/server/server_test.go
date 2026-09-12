package server

import (
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
	instance, err := New(Dependencies{Runtime: newTestRuntime(uuid.New())})
	require.NoError(t, err)

	assert.NotNil(t, instance.httpServer)
}

func TestServerDoesNotServeHTTPMessageSubmission(t *testing.T) {
	sessionID := uuid.New()
	runtime := newTestRuntime(sessionID)
	instance, err := New(Dependencies{
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

// A malformed X-Session-ID header fails oapi-codegen's own UUID parsing
// before the request reaches a handler, distinct from a body shape
// rejection, so it carries its own documented code.
func TestServerRejectsMalformedSessionIDHeader(t *testing.T) {
	instance, err := New(Dependencies{
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
}

func newTestRuntime(sessionID uuid.UUID) *testRuntime {
	return &testRuntime{sessionID: sessionID}
}

func (r *testRuntime) RunMessage(
	_ context.Context,
	_ agent.MessageRequest,
	_ *uuid.UUID,
	_ uuid.UUID,
	sink agent.EventSink,
) (*agent.MessageRunResult, error) {
	r.runCalls++
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

	return &api.ContextSnapshot{Manifest: map[string]any{}}, nil
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
