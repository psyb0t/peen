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
}

// OpenSessionResult is a resolved session and whether this call created it.
type OpenSessionResult struct {
	Session *models.Session
	Created bool
}

// StartTurnInput describes the durable beginning of an agent turn.
type StartTurnInput struct {
	RequestID uuid.UUID
	Workspace string
	Messages  []MessageInput
	Events    []EventInput
}

// Lease uniquely identifies one active turn in a session.
type Lease struct {
	SessionID uuid.UUID
	TurnID    uuid.UUID
}

// MessageInput describes a durable transcript record.
type MessageInput struct {
	ID            uuid.UUID
	Workspace     string
	Role          models.MessageRole
	Content       string
	ModelID       string
	Thinking      string
	ToolCallsJSON string
	ToolCallID    string
	IsError       bool
	Incomplete    bool
}

// EventInput describes one exact internal or protocol event.
type EventInput struct {
	ID               uuid.UUID
	RequestID        uuid.UUID
	EventType        string
	PayloadJSON      string
	ParentToolCallID string
}

// FinalizeTurnInput describes terminal turn state and durable references.
type FinalizeTurnInput struct {
	State                 models.TurnState
	FailureClassification string
	ContextSnapshotHash   *string
	PromptSnapshotHash    *string
	Messages              []MessageInput
	Events                []EventInput
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

// CompactionInput stores one immutable replacement summary.
type CompactionInput struct {
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

// StartAgentRunInput describes the immutable child-agent launch record.
type StartAgentRunInput struct {
	ID               uuid.UUID
	ParentTurnID     uuid.UUID
	ParentAgentRunID *uuid.UUID
	ParentToolCallID string
	RequestID        uuid.UUID
	Name             string
	Definition       models.AgentRunDefinition
	Depth            int64
	Workspace        string
	ModelReference   string
	ModelID          string
	Task             string
	Instructions     string
	AllowedToolsJSON string
	SystemPrompt     string
	StartedAt        time.Time
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
	ID          uuid.UUID
	EventType   string
	PayloadJSON string
	CreatedAt   time.Time
}

// ListAgentRunsOptions controls a bounded child-run page.
type ListAgentRunsOptions struct {
	Limit  int
	Offset int
	State  *models.AgentRunState
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

// ListEventsOptions controls a bounded page of durable protocol events.
type ListEventsOptions struct {
	Limit  int
	Offset int
	Order  PageOrder
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
	Limit  int
	Offset int
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
	ID         uuid.UUID
	TurnID     uuid.UUID
	ToolCallID string
	PID        int64
	Purpose    string
	Command    string
	Directory  string
	StartedAt  time.Time
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
	turnID          uuid.UUID
	cancel          context.CancelFunc
	cancelRequested bool
}
