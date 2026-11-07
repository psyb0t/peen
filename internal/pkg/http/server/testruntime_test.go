package server

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/agent"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
)

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
	feedErr    error
	feedPage   *session.FeedPage
	feedLimit  int32

	// cancelSessionCalls counts CancelSession invocations, so a disconnect
	// test can prove the durable stop boundary was never reached.
	cancelSessionCalls int

	agentRunsErr           error
	agentRunCancelResponse *api.AgentRunCancelResponse

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
	r.cancelSessionCalls++

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

func (r *testRuntime) ListSessionFeed(
	_ context.Context,
	_ api.ListSessionFeedParams,
) (*session.FeedPage, int32, error) {
	if r.feedErr != nil {
		return nil, 0, r.feedErr
	}

	if r.feedPage != nil {
		return r.feedPage, r.feedLimit, nil
	}

	return &session.FeedPage{}, r.feedLimit, nil
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
	if r.agentRunCancelResponse != nil {
		return r.agentRunCancelResponse, nil
	}

	return &api.AgentRunCancelResponse{
		AgentRunId:      agentRunID,
		CancelRequested: true,
		State:           api.AgentRunCancelResponseStateRunning,
	}, nil
}

var _ agent.API = (*testRuntime)(nil)
