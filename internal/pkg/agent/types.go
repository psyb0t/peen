package agent

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/essessey"
	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/metrics"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/tools"
)

const (
	defaultSystemPrompt = "You are a coding agent. Use the available tools to inspect and change the workspace. Read every existing regular file before changing, moving, or deleting it. Check that every new path is absent before creating or moving to it, and never overwrite a destination. Use file tools instead of shell commands for manual file changes. Treat ordering terms in active instructions, including before and after, as hard requirements: satisfy every prerequisite before a mutation. Keep changes within the user's request and preserve unrelated work. Do not invent file contents or claim success without evidence. Verify completed work with the available tools. Run tools without asking for permission. Only pause for approval when an activated skill explicitly requires it. Keep the final response concise: outcome, proof, and any real blocker." //nolint:lll // Prompt text must remain byte-exact.

	// EventTypeTurnStarted marks durable turn initialization.
	EventTypeTurnStarted = "turn.started"
	// EventTypeUserMessageQueued records an accepted message waiting for an
	// active turn's next eligible provider round.
	EventTypeUserMessageQueued = "user_message.queued"
	// EventTypeTextDelta carries one assistant text fragment.
	EventTypeTextDelta = "text.delta"
	// EventTypeThinkingDelta carries one assistant reasoning fragment.
	EventTypeThinkingDelta = "thinking.delta"
	// EventTypeProviderRetry records a retriable upstream attempt.
	EventTypeProviderRetry = "provider.retry"
	// EventTypeToolUse carries one requested tool call.
	EventTypeToolUse = "tool.use"
	// EventTypeToolResult carries the outcome of one tool call.
	EventTypeToolResult = "tool.result"
	// EventTypeSessionEvents carries session events delivered to the model.
	EventTypeSessionEvents = "session.events"
	// EventTypeTurnCompleted marks a completed durable turn.
	EventTypeTurnCompleted = "turn.completed"
	// EventTypeTurnFailed marks a non-cancellation failed turn.
	EventTypeTurnFailed = "turn.failed"
	// EventTypeTurnCancelled marks a cancelled durable turn.
	EventTypeTurnCancelled = "turn.cancelled"

	eventTypeTurnStarted   = EventTypeTurnStarted
	eventTypeTextDelta     = EventTypeTextDelta
	eventTypeThinkingDelta = EventTypeThinkingDelta
	eventTypeTurnCompleted = EventTypeTurnCompleted

	failureClassAgentRun  = "agent_run"
	failureClassCancelled = "cancelled"

	reasonCheckpointFailed = "turn_checkpoint_failed"
	systemSectionGap       = "\n\n"

	runtimeContextHeader              = "Trusted runtime context:"
	runtimeContextLocalTimeLead       = "Current local time: "
	runtimeContextTimezoneLead        = "Local timezone: "
	runtimeContextOperatingSystemLead = "Operating system: "
	runtimeContextArchitectureLead    = "Architecture: "
	runtimeContextLogicalCPUsLead     = "Logical CPUs: "
	runtimeContextGoRuntimeLead       = "Go runtime: "
	//nolint:lll // Prompt guidance must remain byte-exact.
	runtimeContextFreshnessGuidance = "Treat this as the current-time reference. Verify external facts that may have changed."

	// workspaceMetadataLead introduces the JSON-encoded workspace path that
	// every relative tool path resolves from.
	workspaceMetadataLead = "Current message workspace, the directory " +
		"relative tool paths resolve from: "

	defaultMaxSystemPromptBytes  = 65536
	defaultMaxMessageBytes       = 262144
	defaultMaxConcurrentTurns    = 16
	defaultMaxQueuedUserMessages = 16

	// compactionSummaryLead marks the synthetic history message as a summary
	// of earlier conversation rather than something the user just said.
	compactionSummaryLead = "Summary of earlier conversation in this " +
		"session, replacing the messages it covers:\n"

	promptSectionAdditionalCapacity = 2

	defaultMaxToolRounds       = 32
	defaultMaxConcurrentTools  = 4
	defaultToolTimeout         = 15 * time.Minute
	defaultMaxToolResultTokens = 8192

	defaultCompactionOutputTokens = 2048
	defaultCompactionTimeout      = 2 * time.Minute

	// sessionEventTimeLayout is RFC3339 in UTC, matching the API's timestamps.
	sessionEventTimeLayout = time.RFC3339

	// turnStartMessageCapacity covers the user message plus an optional
	// pending-events message delivered ahead of it.
	turnStartMessageCapacity = 2
)

// PromptMode controls how a per-turn system prompt changes the base prompt.
type PromptMode string

const (
	// PromptModeAppend adds the supplied prompt after Peen's base prompt.
	PromptModeAppend PromptMode = "append"
	// PromptModeReplace replaces Peen's base prompt for only the current turn.
	PromptModeReplace PromptMode = "replace"
)

// Event is one transport-neutral visible agent event.
type Event struct {
	Type    string
	Payload json.RawMessage
}

// EventSink observes events as Peen produces them. Returning an error aborts
// the active turn so a disconnected streaming transport can stop provider work.
type EventSink func(Event) error

// TurnOrigin records why a turn exists when no user asked for one. A reader
// looking at a session cannot otherwise tell which of several queued events
// caused a turn to start.
type TurnOrigin struct {
	EventID   uuid.UUID
	EventType string
}

// TurnRequest describes one user message and its optional per-turn settings.
type TurnRequest struct {
	SessionID        *uuid.UUID
	RequestID        uuid.UUID
	Message          string
	Workspace        string
	Model            string
	SystemPrompt     string
	SystemPromptMode PromptMode
	OnEvent          EventSink

	// Origin is set only for a turn an event started. A caller-sent message
	// leaves it nil.
	Origin *TurnOrigin
}

// TurnResult contains the durable session identity and the final agent output.
type TurnResult struct {
	SessionID uuid.UUID
	Created   bool
	// Queued reports that the input was accepted by a currently active turn.
	// A queued result has no final model output because the original turn owns
	// the next eligible provider round.
	Queued   bool
	Text     string
	Thinking string
	Model    string
	Events   []Event

	// FinishReason, HasToolCalls, and OutputTokens describe how the turn
	// ended. The stream epilogue reports them, so a client can tell a
	// complete answer from one the provider cut off, rather than being told
	// every turn ended cleanly.
	FinishReason elelem.FinishReason
	HasToolCalls bool
	OutputTokens int64
}

// MessageRunResult carries a generated JSON response and durable header data.
type MessageRunResult struct {
	Response  api.MessageResponse
	SessionID uuid.UUID
	Queued    bool
}

// StreamMessageResult carries a generated SSE body and durable header data.
type StreamMessageResult struct {
	Body      io.ReadCloser
	SessionID uuid.UUID
	Queued    bool
}

// RuntimeOptions supplies Peen's transport-independent turn dependencies.
type RuntimeOptions struct {
	Store            *session.Store
	Resolver         HarnessResolver
	Models           ModelResolver
	RootAgent        string
	DefaultModel     string
	DefaultWorkspace string
	MaxContextTokens int
	TurnTimeout      time.Duration

	// BaseSystemPrompt overrides the deployment prompt. Empty reads SYSTEM.md
	// and APPEND_SYSTEM.md from ConfigDirectory and otherwise takes the
	// embedded default.
	BaseSystemPrompt string

	// MaxSystemPromptBytes bounds a request's own prompt text. Zero takes the
	// package default.
	MaxSystemPromptBytes int

	// MaxConcurrentTurns bounds turns running at once across every session.
	// Zero takes the package default; negative disables the bound.
	MaxConcurrentTurns int

	// MaxMessageBytes bounds one caller-supplied message. Zero takes the
	// package default.
	MaxMessageBytes int

	// MaxQueuedUserMessages bounds user messages accepted while a session's
	// turn is active. Zero takes the package default.
	MaxQueuedUserMessages int

	// CompactionMode selects what happens when a request exceeds
	// MaxContextTokens. Empty takes drop-oldest, which installs no Peen hook
	// and lets Elelem evict whole conversation units from that request.
	CompactionMode config.CompactionMode
	// CompactionModel is the qualified model the summarizer runs on. Empty
	// takes DefaultModel.
	CompactionModel string
	// CompactionPrompt overrides the summarizer instructions. Empty reads
	// COMPACTION.md from ConfigDirectory and otherwise takes the embedded
	// default.
	CompactionPrompt string
	// CompactionOutputTokens reserves bounded space for the summary. Zero
	// takes the package default.
	CompactionOutputTokens int
	// CompactionTimeout bounds the separate summarization call. Zero takes
	// the package default.
	CompactionTimeout time.Duration

	// ToolLimits bounds every host tool result. Zero fields take the package
	// defaults.
	ToolLimits tools.Limits
	// MaxToolRounds bounds how many tool rounds one turn may run.
	MaxToolRounds int
	// MaxConcurrentTools bounds parallel tool execution inside one round.
	MaxConcurrentTools int
	// ToolTimeout bounds one tool call, hooks included.
	ToolTimeout time.Duration
	// MaxToolResultTokens bounds one tool result before it enters context.
	MaxToolResultTokens int
	// EnableWorkspaceHooks opts into executable hook actions found in workspace
	// layers. Config-directory hooks always run.
	EnableWorkspaceHooks bool
	// HookCommandTimeout bounds one executable hook action. Zero takes the
	// hook package default.
	HookCommandTimeout time.Duration
	// MaxHookCommandOutput bounds combined stdout or stderr from one executable
	// hook action. Zero takes the hook package default.
	MaxHookCommandOutput int

	// MaxEventWakesPerHour bounds how often events may start turns for one
	// session. Zero or negative disables the bound.
	MaxEventWakesPerHour int

	// Events delivers notices the agent did not ask about: a finished process
	// job, a finished child agent, or an outside report. A nil bus disables
	// delivery, which is what an embedding caller that wants none gets.
	Events *events.Bus
	// Metrics records bounded runtime telemetry. A nil value disables metrics
	// for embedding callers that do not expose an operator scrape endpoint.
	Metrics *metrics.Metrics

	// AgentLimits bounds launch_agent: child depth and turns, concurrent runs
	// per session, and the agent run event ring buffer. Zero fields take the
	// package defaults.
	AgentLimits AgentRunLimits

	// ConfigDirectory is PEEN_CONFIG_DIR, used only to place each agent run's
	// JSONL transcript mirror alongside the session's own. Empty disables the
	// mirror entirely rather than failing, matching a nil Events bus.
	ConfigDirectory string
}

// validate rejects an options set that cannot run a turn. Bounds a caller may
// leave at zero are filled by the with*Defaults helpers instead.
func (o RuntimeOptions) validate() error {
	if o.Store == nil || o.Resolver == nil || o.Models == nil {
		return ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet,
			"runtime dependency",
		)
	}

	if o.RootAgent == "" || o.DefaultModel == "" ||
		o.DefaultWorkspace == "" || o.MaxContextTokens <= 0 ||
		o.TurnTimeout <= 0 || o.MaxQueuedUserMessages < 0 {
		return ctxerrors.Wrap(commerr.ErrValidationFailed, "runtime options")
	}

	return nil
}

// withToolDefaults fills the tool bounds a caller left at zero so an embedding
// Go program does not have to restate every deployment limit.
func (o RuntimeOptions) withToolDefaults() RuntimeOptions {
	if o.MaxToolRounds <= 0 {
		o.MaxToolRounds = defaultMaxToolRounds
	}

	if o.MaxConcurrentTools <= 0 {
		o.MaxConcurrentTools = defaultMaxConcurrentTools
	}

	if o.ToolTimeout <= 0 {
		o.ToolTimeout = defaultToolTimeout
	}

	if o.MaxToolResultTokens <= 0 {
		o.MaxToolResultTokens = defaultMaxToolResultTokens
	}

	return o
}

// HarnessResolver resolves the layered agent context for one workspace.
type HarnessResolver interface {
	Resolve(workspace string) (harness.Snapshot, error)
}

// runtimeTurn owns mutable event and message collection for one run. Tool
// hooks for different calls run concurrently, so every mutation goes through
// the mutex.
type runtimeTurn struct {
	requestID uuid.UUID
	sink      EventSink

	// store and lease make the turn's own progress durable before it ends. A
	// nil store disables checkpointing, which is what a unit test that never
	// opened a database gets.
	store *session.Store
	lease session.Lease

	// checkpointMutex serializes the database writes without holding mutex
	// across them, so a checkpoint never blocks the live stream.
	checkpointMutex sync.Mutex

	// statusReporter advertises stream progress. Only the SSE path sets one;
	// a JSON turn has nobody to advertise to.
	statusReporter statusReporter

	// sinkMutex preserves event and sink ordering without holding mutex while
	// executing caller code. A sink may synchronously queue another user
	// message, which checkpoints this turn and therefore needs mutex itself.
	sinkMutex sync.Mutex
	mutex     sync.Mutex
	events    []Event
	messages  []session.MessageInput
	toolStart map[string]time.Time

	// checkpointedEvents and checkpointedMessages record how much of each
	// slice is already durable, so finalization writes only the tail instead
	// of inserting everything a second time.
	checkpointedEvents   int
	checkpointedMessages int
}

// statusReporter advertises what a turn is doing to whoever is watching it
// live. An event that implies no progress change reports nothing.
type statusReporter interface {
	report(eventType string) error
}

// transcriptSink records every protocol event the publisher fans out.
//
// It is what makes the stored event names the ones that actually went on the
// wire, and it sits on the same MultiSink in both modes, so a JSON turn and an
// SSE turn of the same request store identical protocol records.
type transcriptSink struct {
	turn *runtimeTurn
}

func (s transcriptSink) Emit(_ context.Context, event essessey.Event) error {
	if err := s.turn.emitProtocol(event); err != nil {
		return ctxerrors.Wrap(err, "record protocol event")
	}

	return nil
}

// pendingTranscript is one turn's not-yet-durable transcript plus the marks to
// advance once it is written.
type pendingTranscript struct {
	messages     []session.MessageInput
	events       []session.EventInput
	nextMessages int
	nextEvents   int
}

func (p pendingTranscript) isEmpty() bool {
	return len(p.messages) == 0 && len(p.events) == 0
}

// turnStartedPayload is safe session metadata for streaming clients.
type turnStartedPayload struct {
	SessionID string `json:"sessionId"`
	Model     string `json:"model"`
	Workspace string `json:"workspace"`

	// OriginEventID and OriginEventType are present only on a turn an event
	// started, which is what tells a reader why a turn exists that nobody
	// asked for.
	OriginEventID   string `json:"originEventId,omitempty"`
	OriginEventType string `json:"originEventType,omitempty"`
}

type textDeltaPayload struct {
	Text string `json:"text"`
}

type providerRetryPayload struct {
	Attempt int    `json:"attempt"`
	Reason  string `json:"reason"`
	Status  int    `json:"status"`
	DelayMS int64  `json:"delayMs"`
}

type turnCompletedPayload struct {
	Model string `json:"model"`
	Text  string `json:"text"`
}

type turnFailedPayload struct {
	Reason string `json:"reason"`
}

// toolUsePayload mirrors the Chatz-compatible tool_use content block.
type toolUsePayload struct {
	CallID    string          `json:"callId"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// toolResultPayload mirrors the Chatz-compatible tool_result content block.
type toolResultPayload struct {
	CallID  string `json:"callId"`
	Name    string `json:"name"`
	Content string `json:"content"`
	IsError bool   `json:"isError"`
}

// sessionEventsPayload reports the events delivered to the model in one batch.
type sessionEventsPayload struct {
	Notices []events.Notice `json:"notices"`
	Dropped int             `json:"dropped"`
}

var _ HarnessResolver = harness.Resolver{}
