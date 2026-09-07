package peen

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
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
	// ConfigDirectory must already exist. It holds the durable SQLite store
	// plus the optional SYSTEM.md, APPEND_SYSTEM.md, and COMPACTION.md files.
	ConfigDirectory string
	// DefaultWorkspace is the directory relative tool paths resolve from when
	// a message does not name one. Empty captures the caller's current
	// working directory during New.
	DefaultWorkspace string
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

	// MaxContextTokens bounds one turn's request budget. Zero takes the
	// package default.
	MaxContextTokens int
	// TurnTimeout bounds one turn's provider call. Zero takes the package
	// default.
	TurnTimeout time.Duration
	// MaxConcurrentTurns bounds turns running at once across every session.
	// Zero takes the internal default; negative disables the bound.
	MaxConcurrentTurns int
	// MaxMessageBytes bounds one caller-supplied message. Zero takes the
	// internal default.
	MaxMessageBytes int
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
	// CompactionOutputTokens reserves bounded space for the summary. Zero
	// takes the internal default.
	CompactionOutputTokens int
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
	// EnableWorkspaceHooks opts into executable hook actions from workspace
	// .agents/hooks.yaml files. Config-directory hooks always run.
	EnableWorkspaceHooks bool
	// HookCommandTimeout bounds one executable hook action. Zero takes the
	// internal default.
	HookCommandTimeout time.Duration
	// MaxHookCommandOutput bounds stdout or stderr from an executable hook
	// action. Zero takes the internal default.
	MaxHookCommandOutput int

	// MaxEventWakesPerHour bounds how often an external event may start a
	// turn for one session. Zero or negative disables the bound.
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
	// Message is the user's text. It must not be empty or whitespace-only.
	Message string
	// SessionID resumes an existing session. Empty creates a new one.
	SessionID string
	// Workspace overrides the default workspace for only this message.
	Workspace string
	// SystemPrompt overrides or extends the base system prompt for only this
	// message.
	SystemPrompt *SystemPrompt
}

// MessageResult carries the durable session identity and the final assistant
// message.
type MessageResult struct {
	SessionID string
	Message   string
}

// Event is one transport-neutral Chatz-compatible essessey event, or one of
// Peen's own turn lifecycle events, produced while a turn runs. Type is the
// wire event name and Payload is its JSON-encoded body.
type Event struct {
	Type    string
	Payload json.RawMessage
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
type ListMessagesRequest struct {
	// SessionID must name an existing session.
	SessionID string
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

// SessionDetails is one durable session's read-only metadata.
type SessionDetails struct {
	ID                 string
	Agent              string
	Model              string
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
