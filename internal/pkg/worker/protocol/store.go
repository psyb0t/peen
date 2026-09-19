package protocol

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// releaseTimeout bounds the release a finished turn waits on. Releasing is
// synchronous because the controller may dispatch the next turn as soon as the
// worker answers, and a lease released late would refuse that turn as busy.
const releaseTimeout = 10 * time.Second

// Store is a worker's durable surface.
//
// Every record goes to the controller, which writes it to SQLite and publishes
// it. The worker holds no database handle and no generic control credential.
// The lease and its cancellation stay in the worker's own memory, because the
// worker is the only process that runs turns for its session and cancellation
// has to reach a live goroutine rather than a row.
type Store struct {
	conn         *Conn
	sessionID    uuid.UUID
	generationID uuid.UUID

	activeMutex sync.Mutex
	active      map[uuid.UUID]activeTurn
}

// activeTurn is one running turn and the cancellation that reaches it.
type activeTurn struct {
	turnID          uuid.UUID
	cancel          context.CancelFunc
	cancelRequested bool
}

// NewStore builds a worker's durable surface over one registered connection.
func NewStore(
	conn *Conn,
	sessionID uuid.UUID,
	generationID uuid.UUID,
) *Store {
	return &Store{
		conn:         conn,
		sessionID:    sessionID,
		generationID: generationID,
		active:       map[uuid.UUID]activeTurn{},
	}
}

// A worker's store answers the same durable surface the control plane's does.
var _ session.Storage = (*Store)(nil)

func (s *Store) Get(
	ctx context.Context,
	sessionID uuid.UUID,
) (*models.Session, error) {
	return call[*models.Session](
		ctx,
		s,
		MethodGetSession,
		SessionRequest{SessionID: sessionID},
	)
}

func (s *Store) CreateOrResume(
	ctx context.Context,
	_ *uuid.UUID,
	options session.OpenSessionOptions,
) (*session.OpenSessionResult, error) {
	// The requested ID is ignored on purpose. A worker resumes the one session
	// its controller launched it for and cannot ask for another.
	return call[*session.OpenSessionResult](
		ctx,
		s,
		MethodCreateOrResume,
		CreateOrResumeRequest{SessionID: s.sessionID, Options: options},
	)
}

// OpenWorkspace is a control-plane operation. Only the controller resolves a
// workspace to a session, because that is where the workspace policy lives.
func (s *Store) OpenWorkspace(
	_ context.Context,
	_ string,
	_ session.OpenSessionOptions,
) (*session.OpenSessionResult, error) {
	return nil, controllerOnly("open a workspace")
}

func (s *Store) CompletedHistory(
	ctx context.Context,
	sessionID uuid.UUID,
) (*session.History, error) {
	return call[*session.History](
		ctx,
		s,
		MethodCompletedHistory,
		SessionRequest{SessionID: sessionID},
	)
}

func (s *Store) ListMessages(
	ctx context.Context,
	sessionID uuid.UUID,
	options session.ListMessagesOptions,
) (*session.MessagePage, error) {
	return call[*session.MessagePage](
		ctx,
		s,
		MethodListMessages,
		OptionsRequest[session.ListMessagesOptions]{
			SessionID: sessionID,
			Options:   options,
		},
	)
}

func (s *Store) ListEvents(
	ctx context.Context,
	sessionID uuid.UUID,
	options session.ListEventsOptions,
) (*session.EventPage, error) {
	return call[*session.EventPage](
		ctx,
		s,
		MethodListEvents,
		OptionsRequest[session.ListEventsOptions]{
			SessionID: sessionID,
			Options:   options,
		},
	)
}

// AcquireTurn reserves the durable lease on the controller, then records it
// locally so cancellation can reach this process.
func (s *Store) AcquireTurn(
	ctx context.Context,
	sessionID uuid.UUID,
	input session.StartTurnInput,
) (session.Lease, error) {
	lease, err := call[session.Lease](
		ctx,
		s,
		MethodAcquireTurn,
		InputRequest[session.StartTurnInput]{
			SessionID: sessionID,
			Input:     input,
		},
	)
	if err != nil {
		return session.Lease{}, err
	}

	s.activeMutex.Lock()
	s.active[lease.SessionID] = activeTurn{turnID: lease.TurnID}
	s.activeMutex.Unlock()

	return lease, nil
}

func (s *Store) AppendCheckpoint(
	ctx context.Context,
	lease session.Lease,
	messages []session.MessageInput,
	events []session.EventInput,
) error {
	return callVoid(
		ctx,
		s,
		MethodAppendCheckpoint,
		CheckpointRequest{
			Lease:    lease,
			Messages: messages,
			Events:   events,
		},
	)
}

func (s *Store) FinalizeTurn(
	ctx context.Context,
	lease session.Lease,
	input session.FinalizeTurnInput,
) error {
	return callVoid(
		ctx,
		s,
		MethodFinalizeTurn,
		LeaseInputRequest[session.FinalizeTurnInput]{
			Lease: lease,
			Input: input,
		},
	)
}

// ReleaseTurn drops the local lease and waits for the controller to drop its
// own, so the next accepted message is not refused as busy.
func (s *Store) ReleaseTurn(lease session.Lease) {
	s.activeMutex.Lock()
	active, held := s.active[lease.SessionID]

	if held && active.turnID == lease.TurnID {
		delete(s.active, lease.SessionID)
	}

	s.activeMutex.Unlock()

	if !held {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
	defer cancel()

	if err := callVoid(
		ctx,
		s,
		MethodReleaseTurn,
		LeaseRequest{Lease: lease},
	); err != nil {
		ctxscope.GetLogger(ctx).Error(
			"releasing the controller turn lease failed",
			"session_id", lease.SessionID.String(),
			"turn_id", lease.TurnID.String(),
			"err", err,
		)
	}
}

// RegisterCancellation attaches this process's cancellation to a live lease.
// A cancellation that already arrived fires immediately, so a turn cancelled
// between acquiring the lease and starting work does not run anyway.
func (s *Store) RegisterCancellation(
	lease session.Lease,
	cancel context.CancelFunc,
) {
	s.activeMutex.Lock()

	active, held := s.active[lease.SessionID]
	if !held || active.turnID != lease.TurnID {
		s.activeMutex.Unlock()

		return
	}

	active.cancel = cancel
	s.active[lease.SessionID] = active
	s.activeMutex.Unlock()

	if active.cancelRequested && cancel != nil {
		cancel()
	}
}

func (s *Store) IsActive(sessionID uuid.UUID) bool {
	s.activeMutex.Lock()
	defer s.activeMutex.Unlock()

	_, held := s.active[sessionID]

	return held
}

// Cancel records the request durably, then reaches the running turn in this
// process.
func (s *Store) Cancel(
	ctx context.Context,
	sessionID uuid.UUID,
) (bool, error) {
	result, err := call[BoolResult](
		ctx,
		s,
		MethodCancelSession,
		SessionRequest{SessionID: sessionID},
	)
	if err != nil {
		return false, err
	}

	s.RequestLocalCancellation(sessionID)

	return result.Value, nil
}

// RequestLocalCancellation fires the running turn's cancellation without a
// durable write. The controller calls it through the cancel command, which it
// sends after it has already recorded the request.
func (s *Store) RequestLocalCancellation(sessionID uuid.UUID) bool {
	s.activeMutex.Lock()

	active, held := s.active[sessionID]
	if !held {
		s.activeMutex.Unlock()

		return false
	}

	active.cancelRequested = true
	s.active[sessionID] = active
	cancel := active.cancel
	s.activeMutex.Unlock()

	if cancel != nil {
		cancel()
	}

	return true
}

func (s *Store) ListTurns(
	ctx context.Context,
	sessionID uuid.UUID,
	options session.ListTurnsOptions,
) (*session.TurnPage, error) {
	return call[*session.TurnPage](
		ctx,
		s,
		MethodListTurns,
		OptionsRequest[session.ListTurnsOptions]{
			SessionID: sessionID,
			Options:   options,
		},
	)
}

func (s *Store) CreateModelRun(
	ctx context.Context,
	sessionID uuid.UUID,
	input session.CreateModelRunInput,
) (*models.ModelRun, error) {
	return call[*models.ModelRun](
		ctx,
		s,
		MethodCreateModelRun,
		InputRequest[session.CreateModelRunInput]{
			SessionID: sessionID,
			Input:     input,
		},
	)
}

func (s *Store) FinalizeModelRun(
	ctx context.Context,
	sessionID uuid.UUID,
	modelRunID uuid.UUID,
	input session.FinalizeModelRunInput,
) (*models.ModelRun, error) {
	return call[*models.ModelRun](
		ctx,
		s,
		MethodFinalizeModelRun,
		ChildInputRequest[session.FinalizeModelRunInput]{
			SessionID: sessionID,
			ChildID:   modelRunID,
			Input:     input,
		},
	)
}

func (s *Store) ListModelRuns(
	ctx context.Context,
	sessionID uuid.UUID,
	options session.ListModelRunsOptions,
) (*session.ModelRunPage, error) {
	return call[*session.ModelRunPage](
		ctx,
		s,
		MethodListModelRuns,
		OptionsRequest[session.ListModelRunsOptions]{
			SessionID: sessionID,
			Options:   options,
		},
	)
}

func (s *Store) CreateModelCall(
	ctx context.Context,
	sessionID uuid.UUID,
	modelRunID uuid.UUID,
	input session.CreateModelCallInput,
) (*models.ModelCall, error) {
	return call[*models.ModelCall](
		ctx,
		s,
		MethodCreateModelCall,
		ChildInputRequest[session.CreateModelCallInput]{
			SessionID: sessionID,
			ChildID:   modelRunID,
			Input:     input,
		},
	)
}

func (s *Store) FinalizeModelCall(
	ctx context.Context,
	sessionID uuid.UUID,
	modelRunID uuid.UUID,
	modelCallID uuid.UUID,
	input session.FinalizeModelCallInput,
) (*models.ModelCall, error) {
	return call[*models.ModelCall](
		ctx,
		s,
		MethodFinalizeModelCall,
		NestedChildInputRequest[session.FinalizeModelCallInput]{
			SessionID: sessionID,
			ChildID:   modelRunID,
			NestedID:  modelCallID,
			Input:     input,
		},
	)
}

func (s *Store) RecordModelCallRetries(
	ctx context.Context,
	sessionID uuid.UUID,
	modelRunID uuid.UUID,
	modelCallID uuid.UUID,
	input session.RecordModelCallRetriesInput,
) error {
	return callVoid(
		ctx,
		s,
		MethodRecordModelCallRetries,
		NestedChildInputRequest[session.RecordModelCallRetriesInput]{
			SessionID: sessionID,
			ChildID:   modelRunID,
			NestedID:  modelCallID,
			Input:     input,
		},
	)
}

func (s *Store) ListModelCalls(
	ctx context.Context,
	sessionID uuid.UUID,
	modelRunID uuid.UUID,
	options session.ListModelCallsOptions,
) (*session.ModelCallPage, error) {
	return call[*session.ModelCallPage](
		ctx,
		s,
		MethodListModelCalls,
		ChildOptionsRequest[session.ListModelCallsOptions]{
			SessionID: sessionID,
			ChildID:   modelRunID,
			Options:   options,
		},
	)
}

func (s *Store) CreateJob(
	ctx context.Context,
	sessionID uuid.UUID,
	input session.CreateJobInput,
) (*models.Job, error) {
	return call[*models.Job](
		ctx,
		s,
		MethodCreateJob,
		InputRequest[session.CreateJobInput]{
			SessionID: sessionID,
			Input:     input,
		},
	)
}

func (s *Store) FinalizeJob(
	ctx context.Context,
	sessionID uuid.UUID,
	jobID uuid.UUID,
	input session.FinalizeJobInput,
) (*models.Job, error) {
	return call[*models.Job](
		ctx,
		s,
		MethodFinalizeJob,
		ChildInputRequest[session.FinalizeJobInput]{
			SessionID: sessionID,
			ChildID:   jobID,
			Input:     input,
		},
	)
}

func (s *Store) GetJob(
	ctx context.Context,
	sessionID uuid.UUID,
	jobID uuid.UUID,
) (*models.Job, error) {
	return call[*models.Job](
		ctx,
		s,
		MethodGetJob,
		ChildRequest{SessionID: sessionID, ChildID: jobID},
	)
}

func (s *Store) ListJobs(
	ctx context.Context,
	sessionID uuid.UUID,
	options session.ListJobsOptions,
) (*session.JobPage, error) {
	return call[*session.JobPage](
		ctx,
		s,
		MethodListJobs,
		OptionsRequest[session.ListJobsOptions]{
			SessionID: sessionID,
			Options:   options,
		},
	)
}

func (s *Store) AppendJobOutput(
	ctx context.Context,
	sessionID uuid.UUID,
	jobID uuid.UUID,
	input session.AppendJobOutputInput,
) (*models.JobOutputLine, error) {
	return call[*models.JobOutputLine](
		ctx,
		s,
		MethodAppendJobOutput,
		ChildInputRequest[session.AppendJobOutputInput]{
			SessionID: sessionID,
			ChildID:   jobID,
			Input:     input,
		},
	)
}

func (s *Store) ListJobOutput(
	ctx context.Context,
	sessionID uuid.UUID,
	jobID uuid.UUID,
	options session.ListJobOutputOptions,
) (*session.JobOutputPage, error) {
	return call[*session.JobOutputPage](
		ctx,
		s,
		MethodListJobOutput,
		ChildOptionsRequest[session.ListJobOutputOptions]{
			SessionID: sessionID,
			ChildID:   jobID,
			Options:   options,
		},
	)
}

func (s *Store) RecordJobSignal(
	ctx context.Context,
	sessionID uuid.UUID,
	jobID uuid.UUID,
	input session.RecordJobSignalInput,
) (*models.JobSignalRequest, error) {
	return call[*models.JobSignalRequest](
		ctx,
		s,
		MethodRecordJobSignal,
		ChildInputRequest[session.RecordJobSignalInput]{
			SessionID: sessionID,
			ChildID:   jobID,
			Input:     input,
		},
	)
}

func (s *Store) ListJobSignalRequests(
	ctx context.Context,
	sessionID uuid.UUID,
	jobID uuid.UUID,
	options session.ListJobSignalRequestsOptions,
) (*session.JobSignalRequestPage, error) {
	return call[*session.JobSignalRequestPage](
		ctx,
		s,
		MethodListJobSignalRequests,
		ChildOptionsRequest[session.ListJobSignalRequestsOptions]{
			SessionID: sessionID,
			ChildID:   jobID,
			Options:   options,
		},
	)
}

func (s *Store) CreateAgentRun(
	ctx context.Context,
	sessionID uuid.UUID,
	input session.StartAgentRunInput,
) (*models.AgentRun, error) {
	return call[*models.AgentRun](
		ctx,
		s,
		MethodCreateAgentRun,
		InputRequest[session.StartAgentRunInput]{
			SessionID: sessionID,
			Input:     input,
		},
	)
}

func (s *Store) FinalizeAgentRun(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	input session.FinalizeAgentRunInput,
) (*models.AgentRun, error) {
	return call[*models.AgentRun](
		ctx,
		s,
		MethodFinalizeAgentRun,
		ChildInputRequest[session.FinalizeAgentRunInput]{
			SessionID: sessionID,
			ChildID:   agentRunID,
			Input:     input,
		},
	)
}

func (s *Store) GetAgentRun(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
) (*models.AgentRun, error) {
	return call[*models.AgentRun](
		ctx,
		s,
		MethodGetAgentRun,
		ChildRequest{SessionID: sessionID, ChildID: agentRunID},
	)
}

func (s *Store) ListAgentRuns(
	ctx context.Context,
	sessionID uuid.UUID,
	options session.ListAgentRunsOptions,
) (*session.AgentRunPage, error) {
	return call[*session.AgentRunPage](
		ctx,
		s,
		MethodListAgentRuns,
		OptionsRequest[session.ListAgentRunsOptions]{
			SessionID: sessionID,
			Options:   options,
		},
	)
}

func (s *Store) AppendAgentRunEvent(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	input session.AgentRunEventInput,
) (*models.AgentRunEvent, error) {
	return call[*models.AgentRunEvent](
		ctx,
		s,
		MethodAppendAgentRunEvent,
		ChildInputRequest[session.AgentRunEventInput]{
			SessionID: sessionID,
			ChildID:   agentRunID,
			Input:     input,
		},
	)
}

func (s *Store) ListAgentRunEvents(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	options session.ListAgentRunEventsOptions,
) (*session.AgentRunEventPage, error) {
	return call[*session.AgentRunEventPage](
		ctx,
		s,
		MethodListAgentRunEvents,
		ChildOptionsRequest[session.ListAgentRunEventsOptions]{
			SessionID: sessionID,
			ChildID:   agentRunID,
			Options:   options,
		},
	)
}

func (s *Store) RequestAgentRunCancellation(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
) (*models.AgentRun, bool, error) {
	result, err := call[AgentRunCancellationResult](
		ctx,
		s,
		MethodCancelAgentRun,
		ChildRequest{SessionID: sessionID, ChildID: agentRunID},
	)
	if err != nil {
		return nil, false, err
	}

	return result.Run, result.Requested, nil
}

func (s *Store) CreateCompaction(
	ctx context.Context,
	sessionID uuid.UUID,
	input session.CompactionInput,
) (*models.Compaction, error) {
	return call[*models.Compaction](
		ctx,
		s,
		MethodCreateCompaction,
		InputRequest[session.CompactionInput]{
			SessionID: sessionID,
			Input:     input,
		},
	)
}

func (s *Store) GetCompaction(
	ctx context.Context,
	sessionID uuid.UUID,
	compactionID uuid.UUID,
) (*models.Compaction, error) {
	return call[*models.Compaction](
		ctx,
		s,
		MethodGetCompaction,
		ChildRequest{SessionID: sessionID, ChildID: compactionID},
	)
}

func (s *Store) ListCompactions(
	ctx context.Context,
	sessionID uuid.UUID,
	options session.ListCompactionsOptions,
) (*session.CompactionPage, error) {
	return call[*session.CompactionPage](
		ctx,
		s,
		MethodListCompactions,
		OptionsRequest[session.ListCompactionsOptions]{
			SessionID: sessionID,
			Options:   options,
		},
	)
}

func (s *Store) LatestCompaction(
	ctx context.Context,
	sessionID uuid.UUID,
) (*models.Compaction, error) {
	return call[*models.Compaction](
		ctx,
		s,
		MethodLatestCompaction,
		SessionRequest{SessionID: sessionID},
	)
}

func (s *Store) SaveContextSnapshot(
	ctx context.Context,
	snapshot *models.ContextSnapshot,
) error {
	return callVoid(
		ctx,
		s,
		MethodSaveContextSnapshot,
		SnapshotRequest[*models.ContextSnapshot]{
			SessionID: s.sessionID,
			Snapshot:  snapshot,
		},
	)
}

func (s *Store) GetContextSnapshot(
	ctx context.Context,
	sessionID uuid.UUID,
	hash string,
) (*models.ContextSnapshot, error) {
	return call[*models.ContextSnapshot](
		ctx,
		s,
		MethodGetContextSnapshot,
		HashRequest{SessionID: sessionID, Hash: hash},
	)
}

func (s *Store) SavePromptSnapshot(
	ctx context.Context,
	snapshot *models.PromptSnapshot,
) error {
	return callVoid(
		ctx,
		s,
		MethodSavePromptSnapshot,
		SnapshotRequest[*models.PromptSnapshot]{
			SessionID: s.sessionID,
			Snapshot:  snapshot,
		},
	)
}

func (s *Store) GetPromptSnapshot(
	ctx context.Context,
	sessionID uuid.UUID,
	hash string,
) (*models.PromptSnapshot, error) {
	return call[*models.PromptSnapshot](
		ctx,
		s,
		MethodGetPromptSnapshot,
		HashRequest{SessionID: sessionID, Hash: hash},
	)
}

func (s *Store) CreateSessionNotice(
	ctx context.Context,
	sessionID uuid.UUID,
	input session.CreateSessionNoticeInput,
) (*models.SessionNotice, error) {
	return call[*models.SessionNotice](
		ctx,
		s,
		MethodCreateSessionNotice,
		InputRequest[session.CreateSessionNoticeInput]{
			SessionID: sessionID,
			Input:     input,
		},
	)
}

func (s *Store) ListSessionNotices(
	ctx context.Context,
	sessionID uuid.UUID,
	options session.ListSessionNoticesOptions,
) (*session.NoticePage, error) {
	return call[*session.NoticePage](
		ctx,
		s,
		MethodListSessionNotices,
		OptionsRequest[session.ListSessionNoticesOptions]{
			SessionID: sessionID,
			Options:   options,
		},
	)
}

func (s *Store) DrainSessionNotices(
	ctx context.Context,
	sessionID uuid.UUID,
) ([]*models.SessionNotice, error) {
	return call[[]*models.SessionNotice](
		ctx,
		s,
		MethodDrainSessionNotices,
		SessionRequest{SessionID: sessionID},
	)
}

// RecordTurnWorkerGeneration names this worker as the process that ran a turn.
// The controller refuses any generation but this worker's own.
func (s *Store) RecordTurnWorkerGeneration(
	ctx context.Context,
	lease session.Lease,
	generationID uuid.UUID,
) error {
	return callVoid(
		ctx,
		s,
		MethodRecordTurnWorker,
		RecordTurnWorkerRequest{
			Lease:        lease,
			GenerationID: generationID,
		},
	)
}

// GenerationID is the worker generation this store records turns under.
func (s *Store) GenerationID() uuid.UUID {
	return s.generationID
}

// The remaining lifecycle operations belong to the controller. Startup
// recovery runs once, where the database is, and only the controller writes a
// worker generation record.

func (s *Store) RecoverInterrupted(_ context.Context) (int, error) {
	return 0, controllerOnly("recover interrupted turns")
}

func (s *Store) RecoverInterruptedAgentRuns(
	_ context.Context,
) (int, error) {
	return 0, controllerOnly("recover interrupted agent runs")
}

func (s *Store) RecoverInterruptedJobs(_ context.Context) (int, error) {
	return 0, controllerOnly("recover interrupted jobs")
}

func (s *Store) RecoverInterruptedModelRuns(
	_ context.Context,
) (int, error) {
	return 0, controllerOnly("recover interrupted model runs")
}

func (s *Store) CreateWorkerGeneration(
	_ context.Context,
	_ uuid.UUID,
	_ session.CreateWorkerGenerationInput,
) (*models.WorkerGeneration, error) {
	return nil, controllerOnly("create a worker generation")
}

func (s *Store) UpdateWorkerGenerationState(
	_ context.Context,
	_ uuid.UUID,
	_ string,
	_ string,
) error {
	return controllerOnly("update a worker generation state")
}

func controllerOnly(operation string) error {
	return ctxerrors.Wrapf(
		commerr.ErrPermissionDenied,
		"only the control plane may %s",
		operation,
	)
}

// call issues one durable request and decodes its answer.
//
//nolint:ireturn // The decoded type is the caller's own result struct.
func call[R any](
	ctx context.Context,
	s *Store,
	method Method,
	params any,
) (R, error) {
	result := *new(R)

	payload, err := s.conn.Call(ctx, KindCall, string(method), params)
	if err != nil {
		return result, ctxerrors.Wrapf(err, "worker call %q", method)
	}

	if len(payload) == 0 {
		return result, nil
	}

	if err := json.Unmarshal(payload, &result); err != nil {
		return result, ctxerrors.Wrapf(
			commerr.ErrParseFailed,
			"decode the answer to worker call %q",
			method,
		)
	}

	return result, nil
}

// callVoid issues one durable request whose answer carries no value.
func callVoid(
	ctx context.Context,
	s *Store,
	method Method,
	params any,
) error {
	if _, err := s.conn.Call(
		ctx,
		KindCall,
		string(method),
		params,
	); err != nil {
		return ctxerrors.Wrapf(err, "worker call %q", method)
	}

	return nil
}
