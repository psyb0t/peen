package session

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

const (
	// DefaultPageLimit is the API default for transcript pagination.
	DefaultPageLimit = 50
	// MaximumPageLimit bounds one transcript page at the storage boundary.
	MaximumPageLimit = 200
)

// DirectoryState identifies the last accepted root directory change.
// An empty directory means the session still uses its startup directory.
type DirectoryState struct {
	Directory string `json:"directory"`
	Sequence  int64  `json:"sequence"`
}

type ClaimAgentRunInput struct {
	GenerationID         uuid.UUID            `json:"generationId"`
	PreviousGenerationID string               `json:"previousGenerationId"`
	PreviousState        models.AgentRunState `json:"previousState"`
}

// Options supplies deterministic seams for the transport-neutral store.
type Options struct {
	Clock func() time.Time
	NewID func() uuid.UUID

	// MaxStoredMessageBytes bounds one message row of any role. An assistant
	// or tool message has no caller-facing size limit, so without this a
	// single tool result could grow the transcript without bound. Zero takes
	// the package default.
	MaxStoredMessageBytes int
}

// OpenSessionOptions supplies immutable session attribution on creation.
type OpenSessionOptions struct {
	RootAgent string
	ModelID   string

	// AgentSpace is the agent space a newly created session runs in. Opening
	// an existing session never changes its agent space, so this only applies
	// to creation.
	AgentSpace string
}

// OpenSessionResult is a resolved session and whether this call created it.
type OpenSessionResult struct {
	Session *models.Session
	Created bool
}

// StartTurnInput describes the durable beginning of an agent turn.
type StartTurnInput struct {
	RequestID            uuid.UUID
	Workspace            string
	Messages             []MessageInput
	Events               []EventInput
	NoticeIDs            []uuid.UUID `json:"noticeIds,omitempty"`
	ExpectedStopRevision *int64      `json:"expectedStopRevision,omitempty"`
	// WakeNoticeID binds automatic admission to its retained opening notice.
	WakeNoticeID       *uuid.UUID `json:"wakeNoticeId,omitempty"`
	WorkerGenerationID string     `json:"workerGenerationId,omitempty"`
}

// Lease identifies one holder even when the original turn ID is reused.
type Lease struct {
	ID        uuid.UUID `json:"id"`
	SessionID uuid.UUID `json:"sessionId"`
	TurnID    uuid.UUID `json:"turnId"`
}

// MessageInput describes a durable transcript record.
type MessageInput struct {
	ID            uuid.UUID           `json:"id"`
	Workspace     string              `json:"workspace"`
	Role          models.MessageRole  `json:"role"`
	Content       string              `json:"content"`
	ModelID       string              `json:"modelId"`
	Thinking      string              `json:"thinking"`
	ToolCallsJSON string              `json:"toolCallsJson"`
	ToolCallID    string              `json:"toolCallId"`
	IsError       bool                `json:"isError"`
	Incomplete    bool                `json:"incomplete"`
	Injected      bool                `json:"injected"`
	InjectedKind  models.InjectedKind `json:"injectedKind"`
}

// EventInput describes one exact internal or protocol event.
type EventInput struct {
	ID                 uuid.UUID `json:"id"`
	RequestID          uuid.UUID `json:"requestId"`
	WorkerGenerationID string    `json:"workerGenerationId"`
	EventType          string    `json:"eventType"`
	PayloadJSON        string    `json:"payloadJson"`
	ParentToolCallID   string    `json:"parentToolCallId"`
}

// FinalizeTurnInput describes terminal turn state and durable references.
type FinalizeTurnInput struct {
	State                 models.TurnState `json:"state"`
	FailureClassification string           `json:"failureClassification"`
	ContextSnapshotHash   *string          `json:"contextSnapshotHash"`
	PromptSnapshotHash    *string          `json:"promptSnapshotHash"`
	Messages              []MessageInput   `json:"messages"`
	Events                []EventInput     `json:"events"`
}

// PageOrder controls transcript pagination direction.
type PageOrder string

const (
	PageOrderAscending  PageOrder = "asc"
	PageOrderDescending PageOrder = "desc"
)

// ListMessagesOptions controls a bounded session-local message page.
type ListMessagesOptions struct {
	Limit  int
	Offset int
	Order  PageOrder
}

// MessagePage is one complete limit-offset transcript page.
type MessagePage struct {
	Items   []*models.Message
	Limit   int
	Offset  int
	HasMore bool
}

// AgentRunMessageInput is one prompt-visible record in a child agent's own
// transcript. A successful directory result also checkpoints the run's
// current workspace and system prompt in the same transaction.
type AgentRunMessageInput struct {
	ID            uuid.UUID          `json:"id"`
	Role          models.MessageRole `json:"role"`
	Content       string             `json:"content"`
	ModelID       string             `json:"modelId"`
	Thinking      string             `json:"thinking"`
	ToolCallsJSON string             `json:"toolCallsJson"`
	ToolCallID    string             `json:"toolCallId"`
	IsError       bool               `json:"isError"`
	Incomplete    bool               `json:"incomplete"`
	Directory     string             `json:"directory,omitempty"`
	SystemPrompt  string             `json:"systemPrompt,omitempty"`
}

// ListAgentRunMessagesOptions bounds one child transcript page.
type ListAgentRunMessagesOptions struct {
	Limit  int
	Offset int
}

// AgentRunMessagePage is one complete child transcript page, oldest first.
type AgentRunMessagePage struct {
	Items   []*models.AgentRunMessage
	Limit   int
	Offset  int
	HasMore bool
}

// AgentRunHistoryResult is a child's current summary plus the raw tail after
// it, which is what rebuilds the child conversation from SQLite.
type AgentRunHistoryResult struct {
	Compaction *models.AgentRunCompaction
	Messages   []*models.AgentRunMessage
}

// AgentRunCompactionInput stores one immutable child replacement summary.
type AgentRunCompactionInput struct {
	ID                     uuid.UUID
	FromMessageID          uuid.UUID
	ToMessageID            uuid.UUID
	FromSequence           int64
	ToSequence             int64
	DirectFromSequence     int64
	DirectToSequence       int64
	Summary                string
	SourceMessageCount     int64
	InputTokenCount        int64
	SummaryTokenCount      int64
	ModelID                string
	PromptHash             string
	SupersedesCompactionID *uuid.UUID
}

// ListAgentRunCompactionsOptions bounds one child compaction page.
type ListAgentRunCompactionsOptions struct {
	Limit  int
	Offset int
}

// AgentRunCompactionPage is one complete child compaction page, newest first.
type AgentRunCompactionPage struct {
	Items   []*models.AgentRunCompaction
	Limit   int
	Offset  int
	HasMore bool
}

// CompactionInput stores one immutable replacement summary.
type CompactionInput struct {
	ID                     uuid.UUID  `json:"id"`
	FromMessageID          uuid.UUID  `json:"fromMessageId"`
	ToMessageID            uuid.UUID  `json:"toMessageId"`
	FromSequence           int64      `json:"fromSequence"`
	ToSequence             int64      `json:"toSequence"`
	DirectFromSequence     int64      `json:"directFromSequence"`
	DirectToSequence       int64      `json:"directToSequence"`
	Summary                string     `json:"summary"`
	SourceMessageCount     int64      `json:"sourceMessageCount"`
	InputTokenCount        int64      `json:"inputTokenCount"`
	SummaryTokenCount      int64      `json:"summaryTokenCount"`
	ModelID                string     `json:"modelId"`
	PromptHash             string     `json:"promptHash"`
	SupersedesCompactionID *uuid.UUID `json:"supersedesCompactionId"`
}

// StartAgentRunInput describes the immutable child-agent launch record.
type StartAgentRunInput struct {
	ID                 uuid.UUID
	ParentTurnID       uuid.UUID
	WorkerGenerationID string
	ParentAgentRunID   *uuid.UUID
	ParentToolCallID   string
	RequestID          uuid.UUID
	Name               string
	Definition         models.AgentRunDefinition
	Depth              int64
	Workspace          string
	ModelReference     string
	ModelID            string
	Task               string
	Instructions       string
	AllowedToolsJSON   string
	SystemPrompt       string
	StartedAt          time.Time
}

// FinalizeAgentRunInput describes the durable terminal outcome of a child.
type FinalizeAgentRunInput struct {
	State                 models.AgentRunState
	ResponseText          string
	ResponseThinking      string
	ResponseMessagesJSON  string
	FinishReason          string
	PromptTokenCount      int64
	CompletionTokenCount  int64
	FailureClassification string
	FailureDetail         string
}

// AgentRunEventInput is one immutable child-agent event.
type AgentRunEventInput struct {
	ID          uuid.UUID `json:"id"`
	EventType   string    `json:"eventType"`
	PayloadJSON string    `json:"payloadJson"`
	CreatedAt   time.Time `json:"createdAt"`
}

// ListAgentRunsOptions controls a bounded child-run page.
type ListAgentRunsOptions struct {
	Limit  int
	Offset int
	State  *models.AgentRunState

	// IsWaiting keeps only running runs with a question nobody answered yet.
	IsWaiting bool
	// ParentAgentRunID keeps only the direct children of one run.
	ParentAgentRunID *uuid.UUID
	// IsTopLevel keeps only the children the root conversation launched.
	IsTopLevel bool
}

// AgentRunPage is one complete child-run page.
type AgentRunPage struct {
	Items   []*models.AgentRun
	Limit   int
	Offset  int
	HasMore bool
}

// ListAgentRunEventsOptions controls one replay window. Cursor is the next
// sequence number the caller has not seen yet, beginning at zero.
type ListAgentRunEventsOptions struct {
	Cursor int64
	Limit  int
}

// AgentRunEventPage is one complete ordered child-event window.
type AgentRunEventPage struct {
	Run        *models.AgentRun
	Items      []*models.AgentRunEvent
	NextCursor int64
	HasMore    bool
}

// ListTurnsOptions controls a bounded page of durable session turns.
type ListTurnsOptions struct {
	Limit  int
	Offset int
}

// TurnPage is one complete session-local turn page.
type TurnPage struct {
	Items   []*models.Turn
	Limit   int
	Offset  int
	HasMore bool
}

// ListWorkerGenerationsOptions controls a bounded page of one session's
// worker generations.
type ListWorkerGenerationsOptions struct {
	Limit  int
	Offset int
}

// WorkerGenerationPage is one complete page of worker generations.
type WorkerGenerationPage struct {
	Items   []*models.WorkerGeneration
	Limit   int
	Offset  int
	HasMore bool
}

// ListAgentSpaceDecisionsOptions controls a bounded page of one session's
// agent space decisions.
type ListAgentSpaceDecisionsOptions struct {
	Limit  int
	Offset int
}

// AgentSpaceDecisionPage is one complete page of agent space decisions.
type AgentSpaceDecisionPage struct {
	Items   []*models.SessionAgentSpaceDecision
	Limit   int
	Offset  int
	HasMore bool
}

// ListSessionsOptions controls a bounded page of the control surface's
// durable sessions.
type ListSessionsOptions struct {
	Limit  int
	Offset int
}

// WorkspaceSessionPage is one complete page of durable sessions. Every session
// is bound to a canonical workspace, so the name says which sessions it holds
// without stuttering as session.SessionPage.
type WorkspaceSessionPage struct {
	Items   []*models.Session
	Limit   int
	Offset  int
	HasMore bool
}

// ListEventsOptions controls a bounded page of durable protocol events.
type ListEventsOptions struct {
	Limit      int
	Offset     int
	Order      PageOrder
	TurnID     uuid.UUID `json:"turnId,omitempty"`
	EventTypes []string  `json:"eventTypes,omitempty"`
}

// EventPage is one complete session-local protocol event page.
type EventPage struct {
	Items   []*models.Event
	Limit   int
	Offset  int
	HasMore bool
}

// ListCompactionsOptions controls a bounded page of compaction records.
type ListCompactionsOptions struct {
	Limit  int
	Offset int
}

// CompactionPage is one complete session-local compaction page.
type CompactionPage struct {
	Items   []*models.Compaction
	Limit   int
	Offset  int
	HasMore bool
}

// CreateSessionNoticeInput describes an external or worker report before it
// is delivered to a model.
type CreateSessionNoticeInput struct {
	ID        uuid.UUID
	Type      string
	Source    string
	Summary   string
	DataJSON  string
	Delivery  models.NoticeDelivery
	CreatedAt time.Time
}

// ListSessionNoticesOptions controls a bounded page of notice history.
type ListSessionNoticesOptions struct {
	ValidateWakeSource bool `json:"validateWakeSource,omitempty"`
	Limit              int
	Offset             int
	ID                 *uuid.UUID         `json:"id,omitempty"`
	State              models.NoticeState `json:"state,omitempty"`
}

// NoticePage is one complete session-local notice page.
type NoticePage struct {
	Items   []*models.SessionNotice
	Limit   int
	Offset  int
	HasMore bool
}

// CreateJobInput records a supervised process before its output is observed.
type CreateJobInput struct {
	ID                 uuid.UUID
	TurnID             uuid.UUID
	AgentRunID         *uuid.UUID
	WorkerGenerationID string
	ToolCallID         string
	PID                int64
	Purpose            string
	Command            string
	Directory          string
	StartedAt          time.Time
}

// AppendJobOutputInput is one immutable line in the combined process stream.
type AppendJobOutputInput struct {
	ID        uuid.UUID
	Stream    models.JobOutputStream
	Content   string
	CreatedAt time.Time
}

// FinalizeJobInput stores a job's terminal outcome exactly once.
type FinalizeJobInput struct {
	State         models.JobState
	ExitCode      int64
	FailureDetail string
}

// RecordJobSignalInput records one accepted or no-op request to stop a job.
type RecordJobSignalInput struct {
	ID          uuid.UUID
	Signal      models.JobSignal
	Accepted    bool
	RequestedAt time.Time
}

// ListJobsOptions controls a bounded session-local process-job page.
type ListJobsOptions struct {
	Limit  int
	Offset int
	State  *models.JobState
}

// JobPage is one complete process-job page.
type JobPage struct {
	Items   []*models.Job
	Limit   int
	Offset  int
	HasMore bool
}

// ListJobOutputOptions controls one ordered process-output replay window.
type ListJobOutputOptions struct {
	Cursor int64
	Limit  int
	Stream *models.JobOutputStream
}

// JobOutputPage is one complete ordered output replay page.
type JobOutputPage struct {
	Job        *models.Job
	Items      []*models.JobOutputLine
	NextCursor int64
	HasMore    bool
}

// ListJobSignalRequestsOptions controls a bounded signal-request history page.
type ListJobSignalRequestsOptions struct {
	Limit  int
	Offset int
}

// JobSignalRequestPage is one complete signal-request history page.
type JobSignalRequestPage struct {
	Job     *models.Job
	Items   []*models.JobSignalRequest
	Limit   int
	Offset  int
	HasMore bool
}

// CreateModelRunInput describes one logical provider invocation before its
// first outbound round starts.
type CreateModelRunInput struct {
	ID                  uuid.UUID
	TurnID              uuid.UUID
	AgentRunID          *uuid.UUID
	Stage               models.ModelRunStage
	ModelReference      string
	ConnectionName      string
	RequestedModelID    string
	RequestSettingsJSON string
	StartedAt           time.Time
}

// FinalizeModelRunInput stores every non-secret result of one logical model
// invocation, including aggregate usage across its provider rounds.
type FinalizeModelRunInput struct {
	State                  models.ModelRunState
	ResponseModelID        string
	ResponseText           string
	ResponseThinking       string
	ResponseMessagesJSON   string
	ResponseInjectionsJSON string
	ResponseUsageJSON      string
	ResponseCostAmount     string
	RetryCostAmount        string
	BilledCostAmount       string
	CostKnown              bool
	FinishReason           string
	FailureClassification  string
	FailureDetail          string
}

// CreateModelCallInput describes one outbound provider round before bytes
// leave Peen.
type CreateModelCallInput struct {
	ID                  uuid.UUID
	Round               int64
	RequestMessagesJSON string
	RequestToolsJSON    string
	StartedAt           time.Time
}

// RecordModelCallRetriesInput persists every retry that Elelem reports while
// a provider round remains active.
type RecordModelCallRetriesInput struct {
	RetryAttemptsJSON string
	RetryAttemptCount int64
}

// FinalizeModelCallInput records the complete result of one provider round.
type FinalizeModelCallInput struct {
	State                   models.ModelCallState
	ResponseMessageJSON     string
	ResponseUsageJSON       string
	RetryAttemptsJSON       string
	RetryAttemptCount       int64
	PromptTokens            int64
	CompletionTokens        int64
	TotalTokens             int64
	ReasoningTokens         int64
	CacheReadTokens         int64
	CacheWriteTokens        int64
	CacheWriteLongTTLTokens int64
	WastedPromptTokens      int64
	WastedCompletionTokens  int64
	WastedTotalTokens       int64
	TotalAttempts           int64
	ResponseModelID         string
	FinishReason            string
	ResponseCostAmount      string
	RetryCostAmount         string
	BilledCostAmount        string
	CostKnown               bool
	FailureClassification   string
	FailureDetail           string
}

// ListModelRunsOptions controls a bounded session-local model-run page.
type ListModelRunsOptions struct {
	Limit  int
	Offset int
	Stage  *models.ModelRunStage
	State  *models.ModelRunState
}

// ModelRunPage is one complete model-run page.
type ModelRunPage struct {
	Items   []*models.ModelRun
	Limit   int
	Offset  int
	HasMore bool
}

// ListModelCallsOptions controls one ordered provider-round page.
type ListModelCallsOptions struct {
	Limit  int
	Offset int
}

// ModelCallPage is one complete provider-round page for a model run.
type ModelCallPage struct {
	Run     *models.ModelRun
	Items   []*models.ModelCall
	Limit   int
	Offset  int
	HasMore bool
}

// History is the active compaction followed by its uncovered completed tail.
type History struct {
	Compaction *models.Compaction
	Messages   []*models.Message
}

type activeTurn struct {
	leaseID         uuid.UUID
	turnID          uuid.UUID
	cancel          context.CancelFunc
	cancelRequested bool
}
