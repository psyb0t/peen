package peen

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/psyb0t/commander"
	"github.com/psyb0t/elelem"
)

// ModelClient is one already-resolved Elelem model made available under a
// qualified "name/model" reference.
//
// Options.Models is this deployment's named Elelem upstream registry: the
// caller has already built or discovered the client, and Peen only resolves
// DefaultModel and CompactionModel references against the map it is given.
type ModelClient struct {
	Client *elelem.Client
	Model  elelem.Model
}

// CompactionMode selects what happens when a turn's transcript exceeds
// Options.MaxContextTokens.
type CompactionMode string

const (
	// CompactionModeDropOldest evicts whole conversation units. It is the
	// effective mode when Options.CompactionMode is empty.
	CompactionModeDropOldest CompactionMode = "drop-oldest"
	// CompactionModeSummarize replaces an eligible completed prefix with a
	// durable summary produced by CompactionModel.
	CompactionModeSummarize CompactionMode = "summarize"
)

// Options supplies Peen's explicit runtime dependencies and validated values.
//
// New never reads environment variables, starts Servicepack, opens an HTTP
// listener, or touches global state; every dependency arrives here.
type Options struct {
	// StateDirectory holds SQLite and hook state separately from harness
	// configuration. Empty uses ConfigDirectory with a "-state" suffix.
	StateDirectory string
	// Workspace selects the initial session. Empty captures the working
	// directory once when New runs.
	Workspace string
	// InMemoryStore keeps the transcript in memory. Hook state uses a private
	// temporary directory removed by Close.
	InMemoryStore      bool
	BaseSystemPrompt   string
	AppendSystemPrompt string
	AskTimeout         time.Duration
	DisabledCoreSkills []string
	DisabledCoreAgents []string
	ToolLimits         ToolLimits
	AgentLimits        AgentLimits
	HarnessLimits      HarnessLimits
	EventLimits        EventLimits
	Tools              []Tool
	DisableTools       []string
	ModelResolver      ModelResolver
	Hooks              map[string]HookHandler
	Filesystem         FS
	WorkspaceID        string
	Harness            []HarnessLayer
	Logger             *slog.Logger
	MetricsRegisterer  prometheus.Registerer
	AuditLog           AuditLogOptions
	CommandRunner      commander.Commander
	// ConfigDirectory must already exist. It holds the optional SYSTEM.md,
	// APPEND_SYSTEM.md, COMPACTION.md and trusted harness files.
	ConfigDirectory string
	// RootAgent names the base agent definition every session starts from.
	RootAgent string

	// Models is this deployment's named Elelem upstream registry. Every
	// qualified "name/model" reference DefaultModel or CompactionModel may
	// select must be a key here.
	Models map[string]ModelClient
	// DefaultModel is the qualified reference the main agent runs on. It must
	// be a key in Models.
	DefaultModel string
	// CompactionModel is the qualified reference the summarizer runs on.
	// Empty takes DefaultModel. A non-empty value must be a key in Models.
	CompactionModel string

	// MaxContextTokens bounds one request's whole context, prompt and reply
	// together. Zero takes the package default.
	MaxContextTokens int
	// OutputReserveTokens is the room kept for the reply inside
	// MaxContextTokens; the prompt gets the rest. Zero takes the package
	// default of 4096.
	OutputReserveTokens int
	// ResumeAfterCrash lets work a previous process left unfinished continue
	// by itself when the runtime starts. False, the default, pauses it until
	// a turn resumes it.
	ResumeAfterCrash bool
	// TurnTimeout bounds one turn's provider call. Zero takes the package
	// default.
	TurnTimeout time.Duration
	// MaxConcurrentTurns bounds turns running at once across every session.
	// Zero takes the internal default; negative disables the bound.
	MaxConcurrentTurns int
	// MaxMessageBytes bounds one caller-supplied message. Zero takes the
	// internal default.
	MaxMessageBytes int
	// MaxQueuedUserMessages bounds messages accepted while a session's turn is
	// active. Zero takes the internal default.
	MaxQueuedUserMessages int
	// MaxSystemPromptBytes bounds one caller-supplied system prompt. Zero
	// takes the internal default.
	MaxSystemPromptBytes int
	// MaxStoredMessageBytes bounds one durable transcript row. Zero takes the
	// internal default.
	MaxStoredMessageBytes int

	// CompactionMode selects what happens over budget. Empty takes
	// CompactionModeDropOldest.
	CompactionMode CompactionMode
	// CompactionPrompt overrides the summarizer instructions. Empty reads
	// COMPACTION.md from ConfigDirectory and otherwise takes the embedded
	// default.
	CompactionPrompt string
	// CompactionOutputTokens caps the space a summary gets. Zero takes the
	// internal default.
	CompactionOutputTokens int
	// CompactionSummaryPercent is the share of MaxContextTokens a summary
	// gets, up to CompactionOutputTokens, from 1 to 50. Zero takes the
	// internal default.
	CompactionSummaryPercent int
	// CompactionTimeout bounds the separate summarization call. Zero takes
	// the internal default.
	CompactionTimeout time.Duration

	// MaxToolRounds bounds how many tool rounds one turn may run. Zero takes
	// the internal default.
	MaxToolRounds int
	// MaxConcurrentTools bounds parallel tool execution inside one round.
	// Zero takes the internal default.
	MaxConcurrentTools int
	// ToolTimeout bounds one tool call, hooks included. Zero takes the
	// internal default.
	ToolTimeout time.Duration
	// MaxToolResultTokens bounds one tool result before it enters context.
	// Zero takes the internal default.
	MaxToolResultTokens int
	// HookCommandTimeout bounds one executable hook action. Zero takes the
	// internal default.
	HookCommandTimeout time.Duration
	// MaxHookCommandOutput bounds stdout or stderr from an executable hook
	// action. Zero takes the internal default.
	MaxHookCommandOutput int

	// MaxEventWakesPerHour bounds how often an external event may start a
	// turn for one session. Zero takes the deployment default; negative
	// disables the bound.
	MaxEventWakesPerHour int

	// Clock and NewID are deterministic seams for tests. Nil takes time.Now
	// and uuid.New respectively.
	Clock func() time.Time
	NewID func() uuid.UUID
}

// PromptMode controls how a per-message system prompt changes the base
// prompt.
type PromptMode string

const (
	// PromptModeAppend adds the supplied prompt after the base prompt.
	PromptModeAppend PromptMode = "append"
	// PromptModeReplace replaces the base prompt for only the current
	// message.
	PromptModeReplace PromptMode = "replace"
)

// SystemPrompt overrides or extends the base system prompt for one message.
type SystemPrompt struct {
	Content string
	Mode    PromptMode
}

// MessageRequest describes one user message and its optional per-message
// settings.
type MessageRequest struct {
	Model           string
	ReasoningEffort string
	RequestID       string
	To              string
	// Message is the user's text. It must not be empty or whitespace-only.
	Message string
	// SystemPrompt overrides or extends the base system prompt for only this
	// message.
	SystemPrompt *SystemPrompt
}

// MessageResult carries a final answer or acceptance into an active turn.
type MessageResult struct {
	Message string
	// Queued reports that an active turn accepted the message. Message is empty
	// because the original turn owns the eventual model response.
	Queued bool
}

// Event is one transport-neutral Chatz-compatible essessey event, or one of
// Peen's own turn lifecycle events, produced while a turn runs. Type is the
// wire event name and Payload is its JSON-encoded body.
type Event struct {
	SessionID string
	RequestID string
	Type      string
	Payload   json.RawMessage
}

// EventSink observes Stream's events in the order Peen produces them.
// Returning an error aborts the active turn.
type EventSink func(Event) error

// MessageOrder controls transcript pagination direction.
type MessageOrder string

const (
	MessageOrderAsc  MessageOrder = "asc"
	MessageOrderDesc MessageOrder = "desc"
)

// ListMessagesRequest describes one bounded transcript page request.
//
// It names no session: this runtime reads its own workspace's transcript.
type ListMessagesRequest struct {
	// Limit bounds the page size. Zero takes the internal default.
	Limit int
	// Offset skips this many messages from the start of Order's direction.
	Offset int
	// Order selects pagination direction. Empty takes MessageOrderAsc.
	Order MessageOrder
}

// MessageRole classifies who or what produced a transcript message.
type MessageRole string

const (
	// MessageRoleSystem is a notice Peen added to the conversation, such as
	// how the previous turn ended.
	MessageRoleSystem    MessageRole = "system"
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
	MessageRoleTool      MessageRole = "tool"
)

// MessageToolCall is one tool call an assistant message requested.
type MessageToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}

// Message is one durable transcript record.
type Message struct {
	ID         string
	Sequence   int64
	Role       MessageRole
	Content    string
	CreatedAt  time.Time
	Workspace  string
	Model      string
	Thinking   string
	ToolCallID string
	ToolCalls  []MessageToolCall
	IsError    bool
	Incomplete bool
}

// ListMessagesResult is one complete limit-offset transcript page.
type ListMessagesResult struct {
	Items   []Message
	Limit   int
	Offset  int
	HasMore bool
}

// SessionDetails reports durable identity, metadata and active work.
type SessionDetails struct {
	ID                 string
	Agent              string
	Model              string
	Workspace          string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	LastMessageAt      *time.Time
	MessageCount       int64
	CompletedTurnCount int64
	ActiveTurn         bool
}

// CancelResult reports whether a cancel request was accepted.
type CancelResult struct {
	CancelRequested bool
}
