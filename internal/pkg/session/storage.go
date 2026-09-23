package session

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

// Storage is the durable surface one session's runtime needs.
//
// It exists because the same runtime runs in two places. Inside the control
// plane it is backed by *Store, which owns SQLite. Inside a worker it is
// backed by the private worker protocol, which forwards each operation to the
// controller. A worker therefore gets the same transcript semantics without
// ever opening the controller's database.
//
// The interface is deliberately the whole set rather than a convenient subset.
// A worker-side implementation must answer every method, and refusing one it
// has no business performing is a decision that shows up in the code rather
// than a method that silently does not exist.
type Storage interface {
	sessionStorage
	turnStorage
	modelStorage
	jobStorage
	agentRunStorage
	agentRunTranscriptStorage
	recordStorage
	lifecycleStorage
}

// sessionStorage reads and opens sessions.
type sessionStorage interface {
	Get(ctx context.Context, sessionID uuid.UUID) (*models.Session, error)
	CreateOrResume(
		ctx context.Context,
		requestedSessionID *uuid.UUID,
		options OpenSessionOptions,
	) (*OpenSessionResult, error)
	OpenWorkspace(
		ctx context.Context,
		workspace string,
		options OpenSessionOptions,
	) (*OpenSessionResult, error)
	CompletedHistory(
		ctx context.Context,
		sessionID uuid.UUID,
	) (*History, error)
	ListMessages(
		ctx context.Context,
		sessionID uuid.UUID,
		options ListMessagesOptions,
	) (*MessagePage, error)
	ListEvents(
		ctx context.Context,
		sessionID uuid.UUID,
		options ListEventsOptions,
	) (*EventPage, error)
}

// turnStorage owns the one-turn-per-session lease and its transcript.
type turnStorage interface {
	AcquireTurn(
		ctx context.Context,
		sessionID uuid.UUID,
		input StartTurnInput,
	) (Lease, error)
	AppendCheckpoint(
		ctx context.Context,
		lease Lease,
		messages []MessageInput,
		events []EventInput,
	) error
	FinalizeTurn(
		ctx context.Context,
		lease Lease,
		input FinalizeTurnInput,
	) error
	ReleaseTurn(lease Lease)
	RegisterCancellation(lease Lease, cancel context.CancelFunc)
	IsActive(sessionID uuid.UUID) bool
	Cancel(ctx context.Context, sessionID uuid.UUID) (bool, error)
	ListTurns(
		ctx context.Context,
		sessionID uuid.UUID,
		options ListTurnsOptions,
	) (*TurnPage, error)
}

// modelStorage records every provider request and response.
type modelStorage interface {
	CreateModelRun(
		ctx context.Context,
		sessionID uuid.UUID,
		input CreateModelRunInput,
	) (*models.ModelRun, error)
	FinalizeModelRun(
		ctx context.Context,
		sessionID uuid.UUID,
		modelRunID uuid.UUID,
		input FinalizeModelRunInput,
	) (*models.ModelRun, error)
	ListModelRuns(
		ctx context.Context,
		sessionID uuid.UUID,
		options ListModelRunsOptions,
	) (*ModelRunPage, error)
	CreateModelCall(
		ctx context.Context,
		sessionID uuid.UUID,
		modelRunID uuid.UUID,
		input CreateModelCallInput,
	) (*models.ModelCall, error)
	FinalizeModelCall(
		ctx context.Context,
		sessionID uuid.UUID,
		modelRunID uuid.UUID,
		modelCallID uuid.UUID,
		input FinalizeModelCallInput,
	) (*models.ModelCall, error)
	RecordModelCallRetries(
		ctx context.Context,
		sessionID uuid.UUID,
		modelRunID uuid.UUID,
		modelCallID uuid.UUID,
		input RecordModelCallRetriesInput,
	) error
	ListModelCalls(
		ctx context.Context,
		sessionID uuid.UUID,
		modelRunID uuid.UUID,
		options ListModelCallsOptions,
	) (*ModelCallPage, error)
}

// jobStorage records background commands and their output.
type jobStorage interface {
	CreateJob(
		ctx context.Context,
		sessionID uuid.UUID,
		input CreateJobInput,
	) (*models.Job, error)
	FinalizeJob(
		ctx context.Context,
		sessionID uuid.UUID,
		jobID uuid.UUID,
		input FinalizeJobInput,
	) (*models.Job, error)
	GetJob(
		ctx context.Context,
		sessionID uuid.UUID,
		jobID uuid.UUID,
	) (*models.Job, error)
	ListJobs(
		ctx context.Context,
		sessionID uuid.UUID,
		options ListJobsOptions,
	) (*JobPage, error)
	AppendJobOutput(
		ctx context.Context,
		sessionID uuid.UUID,
		jobID uuid.UUID,
		input AppendJobOutputInput,
	) (*models.JobOutputLine, error)
	ListJobOutput(
		ctx context.Context,
		sessionID uuid.UUID,
		jobID uuid.UUID,
		options ListJobOutputOptions,
	) (*JobOutputPage, error)
	RecordJobSignal(
		ctx context.Context,
		sessionID uuid.UUID,
		jobID uuid.UUID,
		input RecordJobSignalInput,
	) (*models.JobSignalRequest, error)
	ListJobSignalRequests(
		ctx context.Context,
		sessionID uuid.UUID,
		jobID uuid.UUID,
		options ListJobSignalRequestsOptions,
	) (*JobSignalRequestPage, error)
}

// agentRunStorage records child-agent work.
type agentRunStorage interface {
	CreateAgentRun(
		ctx context.Context,
		sessionID uuid.UUID,
		input StartAgentRunInput,
	) (*models.AgentRun, error)
	FinalizeAgentRun(
		ctx context.Context,
		sessionID uuid.UUID,
		agentRunID uuid.UUID,
		input FinalizeAgentRunInput,
	) (*models.AgentRun, error)
	GetAgentRun(
		ctx context.Context,
		sessionID uuid.UUID,
		agentRunID uuid.UUID,
	) (*models.AgentRun, error)
	ListAgentRuns(
		ctx context.Context,
		sessionID uuid.UUID,
		options ListAgentRunsOptions,
	) (*AgentRunPage, error)
	AppendAgentRunEvent(
		ctx context.Context,
		sessionID uuid.UUID,
		agentRunID uuid.UUID,
		input AgentRunEventInput,
	) (*models.AgentRunEvent, error)
	ListAgentRunEvents(
		ctx context.Context,
		sessionID uuid.UUID,
		agentRunID uuid.UUID,
		options ListAgentRunEventsOptions,
	) (*AgentRunEventPage, error)
	RequestAgentRunCancellation(
		ctx context.Context,
		sessionID uuid.UUID,
		agentRunID uuid.UUID,
	) (*models.AgentRun, bool, error)
}

// agentRunTranscriptStorage records one child agent's own conversation and the
// compactions over it.
//
// A child agent runs a separate model context, so its transcript and its
// compactions are stored apart from the session's own. A parent-session
// compaction can never absorb a child message, and a child compaction can
// never rewrite a session record.
type agentRunTranscriptStorage interface {
	AppendAgentRunMessages(
		ctx context.Context,
		sessionID uuid.UUID,
		agentRunID uuid.UUID,
		inputs []AgentRunMessageInput,
	) ([]*models.AgentRunMessage, error)
	ListAgentRunMessages(
		ctx context.Context,
		sessionID uuid.UUID,
		agentRunID uuid.UUID,
		options ListAgentRunMessagesOptions,
	) (*AgentRunMessagePage, error)
	AgentRunHistory(
		ctx context.Context,
		sessionID uuid.UUID,
		agentRunID uuid.UUID,
	) (*AgentRunHistoryResult, error)
	CreateAgentRunCompaction(
		ctx context.Context,
		sessionID uuid.UUID,
		agentRunID uuid.UUID,
		input AgentRunCompactionInput,
	) (*models.AgentRunCompaction, error)
	LatestAgentRunCompaction(
		ctx context.Context,
		sessionID uuid.UUID,
		agentRunID uuid.UUID,
	) (*models.AgentRunCompaction, error)
	ListAgentRunCompactions(
		ctx context.Context,
		sessionID uuid.UUID,
		agentRunID uuid.UUID,
		options ListAgentRunCompactionsOptions,
	) (*AgentRunCompactionPage, error)
	GetAgentRunCompaction(
		ctx context.Context,
		sessionID uuid.UUID,
		agentRunID uuid.UUID,
		compactionID uuid.UUID,
	) (*models.AgentRunCompaction, error)
}

// recordStorage covers compactions, snapshots, and session notices.
//
//nolint:interfacebloat // One method per durable record kind.
type recordStorage interface {
	CreateCompaction(
		ctx context.Context,
		sessionID uuid.UUID,
		input CompactionInput,
	) (*models.Compaction, error)
	GetCompaction(
		ctx context.Context,
		sessionID uuid.UUID,
		compactionID uuid.UUID,
	) (*models.Compaction, error)
	ListCompactions(
		ctx context.Context,
		sessionID uuid.UUID,
		options ListCompactionsOptions,
	) (*CompactionPage, error)
	LatestCompaction(
		ctx context.Context,
		sessionID uuid.UUID,
	) (*models.Compaction, error)
	SaveContextSnapshot(
		ctx context.Context,
		snapshot *models.ContextSnapshot,
	) error
	GetContextSnapshot(
		ctx context.Context,
		sessionID uuid.UUID,
		hash string,
	) (*models.ContextSnapshot, error)
	SavePromptSnapshot(
		ctx context.Context,
		snapshot *models.PromptSnapshot,
	) error
	GetPromptSnapshot(
		ctx context.Context,
		sessionID uuid.UUID,
		hash string,
	) (*models.PromptSnapshot, error)
	CreateSessionNotice(
		ctx context.Context,
		sessionID uuid.UUID,
		input CreateSessionNoticeInput,
	) (*models.SessionNotice, error)
	ListSessionNotices(
		ctx context.Context,
		sessionID uuid.UUID,
		options ListSessionNoticesOptions,
	) (*NoticePage, error)
	DrainSessionNotices(
		ctx context.Context,
		sessionID uuid.UUID,
	) ([]*models.SessionNotice, error)
}

// lifecycleStorage covers controller startup recovery and worker generation
// records.
//
// A worker-backed implementation refuses these: recovery happens once when the
// controller starts, and only the controller writes a worker generation.
type lifecycleStorage interface {
	RecoverInterrupted(ctx context.Context) (int, error)
	RecoverInterruptedAgentRuns(ctx context.Context) (int, error)
	RecoverInterruptedJobs(ctx context.Context) (int, error)
	RecoverInterruptedModelRuns(ctx context.Context) (int, error)
	CreateWorkerGeneration(
		ctx context.Context,
		sessionID uuid.UUID,
		input CreateWorkerGenerationInput,
	) (*models.WorkerGeneration, error)
	UpdateWorkerGenerationState(
		ctx context.Context,
		generationID uuid.UUID,
		state string,
		failureDetail string,
	) error
	RecordTurnWorkerGeneration(
		ctx context.Context,
		lease Lease,
		generationID uuid.UUID,
	) error
}

// The control plane's own store is the reference implementation.
var _ Storage = (*Store)(nil)
