package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/events"
)

const (
	defaultMaxAgentDepth          = 3
	defaultMaxChildAgentTurns     = 16
	defaultMaxConcurrentAgentRuns = 4
	defaultMaxAgentRunEventCount  = 2000
	defaultMaxAgentRunEventBytes  = 64 * 1024
	// defaultMaxAdHocInstructionBytes matches harness.Limits' MaxFileBytes
	// default (128 KiB) so an ad-hoc agentDefinition and a stored agent file
	// share one effective instruction-byte bound out of the box, per
	// PLAN.md's "Named agents" contract: "Ad-hoc definitions are bounded by
	// the same instruction-byte limit as stored agent files." agent and
	// harness are separate packages with no shared config source, so this is
	// a duplicated literal, not a derived one: a deployment that configures
	// harness.Limits.MaxFileBytes away from its default MUST pass that same
	// value as AgentRunLimits.MaxAdHocInstructionBytes to keep the two bounds
	// equal.
	defaultMaxAdHocInstructionBytes = 128 * 1024

	// agentEventSource identifies this package as the origin of agent.*
	// events, matching tools.jobEventSource for job.* events.
	agentEventSource = "agent.launch_agent"
)

// AgentRunDefinition names whether a run's child came from a stored agent
// file or an inline ad-hoc definition. It is an alias because it carries no
// behavior and crosses the JSON tool boundary as a plain string.
// surface (AgentRun, AgentRunState, AgentRunRegistry, ...) as a
// deliberately consistent family, and to match tools.JobState/
// tools.JobRegistry's naming shape; the stutter is a direct
// consequence of the type also being called AgentRun*, in a package
// named agent.
//
//nolint:revive // Named AgentRun* to match this file's exported
type AgentRunDefinition = string

const (
	// AgentRunDefinitionStored marks a run launched from an effective agent
	// in the resolved catalogue.
	AgentRunDefinitionStored AgentRunDefinition = "stored"
	// AgentRunDefinitionAdHoc marks a run launched from a caller-supplied
	// inline definition never written to disk.
	AgentRunDefinitionAdHoc AgentRunDefinition = "ad-hoc"
)

// AgentRunState names the lifecycle stage of one launch_agent run. It is an
// alias because it carries no behavior and crosses the JSON tool boundary as
// a plain string.
//
//nolint:revive // See the comment on AgentRunDefinition above.
type AgentRunState = string

const (
	// AgentRunStateRunning marks a run whose child conversation has not yet
	// finished.
	AgentRunStateRunning AgentRunState = "running"
	// AgentRunStateCompleted marks a run that returned a final answer.
	AgentRunStateCompleted AgentRunState = "completed"
	// AgentRunStateFailed marks a run that ended in an error other than
	// cancellation.
	AgentRunStateFailed AgentRunState = "failed"
	// AgentRunStateCancelled marks a run stopped by its own cancellation or
	// by the parent turn's context ending.
	AgentRunStateCancelled AgentRunState = "cancelled"
)

// AgentRunLimits bounds launch_agent work. Zero fields take the default.
//
//nolint:revive // See the comment on AgentRunDefinition above.
type AgentRunLimits struct {
	// MaxDepth bounds how many launch_agent calls may nest: the root turn is
	// depth 0, its first child is depth 1, and so on.
	MaxDepth int
	// MaxChildTurns bounds one child conversation's own tool rounds,
	// independent of the parent turn's MaxToolRounds.
	MaxChildTurns int
	// MaxConcurrentRuns bounds how many agent runs may be simultaneously
	// running for one session.
	MaxConcurrentRuns int
	// MaxEventCount bounds how many events one run's ring buffer retains.
	MaxEventCount int
	// MaxEventBytes bounds the same buffer's total payload bytes; whichever
	// bound is hit first evicts the oldest event.
	MaxEventBytes int
	// MaxAdHocInstructionBytes bounds an inline agentDefinition's
	// instructions. This MUST equal the effective harness.Limits.MaxFileBytes
	// bound applied to stored agent files, so ad-hoc and stored agents share
	// one instruction-byte limit. The caller constructing both limit structs
	// is responsible for passing the same configured value to each; this
	// package cannot enforce that on its own because harness and agent share
	// no config source.
	MaxAdHocInstructionBytes int
}

// DefaultAgentRunLimits returns the bounded defaults every deployment starts
// from.
func DefaultAgentRunLimits() AgentRunLimits {
	return AgentRunLimits{
		MaxDepth:                 defaultMaxAgentDepth,
		MaxChildTurns:            defaultMaxChildAgentTurns,
		MaxConcurrentRuns:        defaultMaxConcurrentAgentRuns,
		MaxEventCount:            defaultMaxAgentRunEventCount,
		MaxEventBytes:            defaultMaxAgentRunEventBytes,
		MaxAdHocInstructionBytes: defaultMaxAdHocInstructionBytes,
	}
}

func (l AgentRunLimits) withDefaults() AgentRunLimits {
	defaults := DefaultAgentRunLimits()

	if l.MaxDepth == 0 {
		l.MaxDepth = defaults.MaxDepth
	}

	if l.MaxChildTurns == 0 {
		l.MaxChildTurns = defaults.MaxChildTurns
	}

	if l.MaxConcurrentRuns == 0 {
		l.MaxConcurrentRuns = defaults.MaxConcurrentRuns
	}

	if l.MaxEventCount == 0 {
		l.MaxEventCount = defaults.MaxEventCount
	}

	if l.MaxEventBytes == 0 {
		l.MaxEventBytes = defaults.MaxEventBytes
	}

	if l.MaxAdHocInstructionBytes == 0 {
		l.MaxAdHocInstructionBytes = defaults.MaxAdHocInstructionBytes
	}

	return l
}

func (l AgentRunLimits) validate() error {
	positives := []int{
		l.MaxDepth,
		l.MaxChildTurns,
		l.MaxConcurrentRuns,
		l.MaxEventCount,
		l.MaxEventBytes,
		l.MaxAdHocInstructionBytes,
	}

	for _, value := range positives {
		if value <= 0 {
			return ctxerrors.Wrap(
				ErrInvalidAgentRunLimits,
				"bound must be positive",
			)
		}
	}

	return nil
}

// AgentRunEvent is one event captured in an agent run's ring buffer, in the
// same type/payload shape the top-level turn's own tool blocks use.
//
//nolint:revive // See the comment on AgentRunDefinition above.
type AgentRunEvent struct {
	Sequence  int
	Type      string
	Payload   json.RawMessage
	CreatedAt time.Time
}

// agentRunEventBuffer is a bounded ring of one agent run's most recent
// events, mirroring tools' unexported jobBuffer in shape: bounded by BOTH
// event count and total payload bytes, oldest dropped first. The tools
// package owns process output buffering; this is the equivalent for a child
// agent's own event stream, kept in this package because launch_agent events
// are never process output.
type agentRunEventBuffer struct {
	mu       sync.Mutex
	maxCount int
	maxBytes int
	events   []AgentRunEvent
	byteLen  int
	total    int
}

func newAgentRunEventBuffer(maxCount, maxBytes int) *agentRunEventBuffer {
	return &agentRunEventBuffer{maxCount: maxCount, maxBytes: maxBytes}
}

// Append records one more event. It never blocks: callers are the child
// conversation's own hook goroutine, which must not stall on a full buffer.
func (b *agentRunEventBuffer) Append(
	eventType string,
	payload json.RawMessage,
) AgentRunEvent {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.total++

	event := AgentRunEvent{
		Sequence:  b.total,
		Type:      eventType,
		Payload:   append(json.RawMessage(nil), payload...),
		CreatedAt: time.Now().UTC(),
	}

	b.events = append(b.events, event)
	b.byteLen += len(event.Type) + len(event.Payload)

	for len(b.events) > 1 && b.overBoundLocked() {
		removed := b.events[0]
		b.byteLen -= len(removed.Type) + len(removed.Payload)
		b.events = b.events[1:]
	}

	return event
}

// AppendStored retains a durable event in the live convenience buffer without
// inventing another sequence or timestamp.
func (b *agentRunEventBuffer) AppendStored(event AgentRunEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if event.Sequence <= b.total {
		return
	}

	b.total = event.Sequence
	stored := AgentRunEvent{
		Sequence:  event.Sequence,
		Type:      event.Type,
		Payload:   append(json.RawMessage(nil), event.Payload...),
		CreatedAt: event.CreatedAt,
	}
	b.events = append(b.events, stored)
	b.byteLen += len(stored.Type) + len(stored.Payload)

	for len(b.events) > 1 && b.overBoundLocked() {
		removed := b.events[0]
		b.byteLen -= len(removed.Type) + len(removed.Payload)
		b.events = b.events[1:]
	}
}

func (b *agentRunEventBuffer) overBoundLocked() bool {
	return len(b.events) > b.maxCount || b.byteLen > b.maxBytes
}

func (b *agentRunEventBuffer) firstIndexLocked() int {
	return b.total - len(b.events)
}

// Read returns up to maxEvents events starting at cursor, the cursor to
// resume from on the next call, and how many events were dropped before the
// window because they had already been evicted. maxEvents <= 0 means no cap.
func (b *agentRunEventBuffer) Read(
	cursor, maxEvents int,
) ([]AgentRunEvent, int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	first := b.firstIndexLocked()

	dropped := 0
	if cursor < first {
		dropped = first - cursor
		cursor = first
	}

	offset := min(cursor-first, len(b.events))

	available := b.events[offset:]
	if maxEvents > 0 && len(available) > maxEvents {
		available = available[:maxEvents]
	}

	result := append([]AgentRunEvent(nil), available...)

	return result, cursor + len(result), dropped
}

// Stats reports how many events are currently buffered and how many have
// been dropped since the buffer was created.
func (b *agentRunEventBuffer) Stats() (int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return len(b.events), b.total - len(b.events)
}

// AgentRun is one execution of a named or ad-hoc child agent launched by
// launch_agent. The identity fields are set once at creation and never
// change; state and EndedAt are read and written through mu so a concurrent
// reader never observes a torn update.
//
//nolint:revive // See the comment on AgentRunDefinition above.
type AgentRun struct {
	ID               uuid.UUID
	SessionID        uuid.UUID
	ParentTurnID     uuid.UUID
	ParentAgentRunID *uuid.UUID
	ParentToolCallID string
	RequestID        uuid.UUID
	Name             string
	Definition       AgentRunDefinition
	Depth            int
	StartedAt        time.Time

	events *agentRunEventBuffer
	cancel context.CancelFunc
	done   chan struct{}

	mu      sync.RWMutex
	state   AgentRunState
	endedAt time.Time
}

// AgentRunSnapshot is one point-in-time, race-free view of an agent run.
//
//nolint:revive // See the comment on AgentRunDefinition above.
type AgentRunSnapshot struct {
	ID                 uuid.UUID
	SessionID          uuid.UUID
	ParentTurnID       uuid.UUID
	ParentAgentRunID   *uuid.UUID
	ParentToolCallID   string
	RequestID          uuid.UUID
	Name               string
	Definition         AgentRunDefinition
	Depth              int
	State              AgentRunState
	StartedAt          time.Time
	EndedAt            time.Time
	EventBufferedCount int
	EventDroppedCount  int
}

// Snapshot copies every field a caller outside this package may read, so a
// future HTTP handler never touches an AgentRun's internal locks directly.
func (a *AgentRun) Snapshot() AgentRunSnapshot {
	a.mu.RLock()
	state, endedAt := a.state, a.endedAt
	a.mu.RUnlock()

	buffered, dropped := a.events.Stats()

	return AgentRunSnapshot{
		ID:                 a.ID,
		SessionID:          a.SessionID,
		ParentTurnID:       a.ParentTurnID,
		ParentAgentRunID:   cloneAgentRunID(a.ParentAgentRunID),
		ParentToolCallID:   a.ParentToolCallID,
		RequestID:          a.RequestID,
		Name:               a.Name,
		Definition:         a.Definition,
		Depth:              a.Depth,
		State:              state,
		StartedAt:          a.StartedAt,
		EndedAt:            endedAt,
		EventBufferedCount: buffered,
		EventDroppedCount:  dropped,
	}
}

// ReadEvents returns a bounded window of this run's recent events, so a
// caller can follow a child in flight without waiting for it to finish.
func (a *AgentRun) ReadEvents(
	cursor, maxEvents int,
) ([]AgentRunEvent, int, int) {
	return a.events.Read(cursor, maxEvents)
}

// AppendStoredEvent refreshes the in-process convenience buffer from an event
// that SQLite already committed. The database remains the replay source.
func (a *AgentRun) AppendStoredEvent(event AgentRunEvent) {
	a.events.AppendStored(event)
}

// Done reports the channel this package closes exactly once, the moment the
// run leaves AgentRunStateRunning.
func (a *AgentRun) Done() <-chan struct{} {
	return a.done
}

// AgentRunRegistry owns every agent run for one session, deliberately
// outliving any single turn so a caller can list, follow, and cancel a run
// after the turn that launched it has moved on. It mirrors
// tools.JobRegistry's shape: an ID, a bounded per-run ring buffer,
// cancellation, and completion publishing.
//
//nolint:revive // See the comment on AgentRunDefinition above.
type AgentRunRegistry struct {
	sessionID uuid.UUID
	publisher events.Publisher
	limits    AgentRunLimits

	mu   sync.RWMutex
	runs map[uuid.UUID]*AgentRun
}

// NewAgentRunRegistry builds a session-scoped agent run registry. publisher
// may be nil, meaning completion events are not published.
func NewAgentRunRegistry(
	sessionID uuid.UUID,
	publisher events.Publisher,
	limits AgentRunLimits,
) (*AgentRunRegistry, error) {
	if sessionID == uuid.Nil {
		return nil, ctxerrors.Wrap(
			ErrInvalidAgentRunOptions,
			"agent run registry session is required",
		)
	}

	resolved := limits.withDefaults()
	if err := resolved.validate(); err != nil {
		return nil, ctxerrors.Wrap(err, "validate agent run limits")
	}

	return &AgentRunRegistry{
		sessionID: sessionID,
		publisher: publisher,
		limits:    resolved,
		runs:      map[uuid.UUID]*AgentRun{},
	}, nil
}

// StartAgentRunInput describes one launch_agent invocation about to run.
type StartAgentRunInput struct {
	ID               uuid.UUID
	ParentTurnID     uuid.UUID
	ParentAgentRunID *uuid.UUID
	ParentToolCallID string
	RequestID        uuid.UUID
	Name             string
	Definition       AgentRunDefinition
	Depth            int
	StartedAt        time.Time
}

// Start registers one agent run in AgentRunStateRunning and returns it along
// with a context descending from ctx. Cancelling ctx (the parent turn ending)
// cancels the returned context too, so the whole child tree stops; cancelling
// only this run later, through Cancel, leaves ctx and every sibling run
// alone. An error means the session is already at its concurrent-run bound.
func (r *AgentRunRegistry) Start(
	ctx context.Context,
	input StartAgentRunInput,
	persist ...func(*AgentRun) error,
) (*AgentRun, context.Context, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.runningCountLocked() >= r.limits.MaxConcurrentRuns {
		return nil, nil, ctxerrors.Wrapf(
			ErrTooManyAgentRuns,
			"session already runs %d concurrent agent runs",
			r.limits.MaxConcurrentRuns,
		)
	}

	runCtx, cancel := context.WithCancel(ctx)

	runID := input.ID
	if runID == uuid.Nil {
		runID = uuid.New()
	}

	startedAt := input.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	} else {
		startedAt = startedAt.UTC()
	}

	run := &AgentRun{
		ID:               runID,
		SessionID:        r.sessionID,
		ParentTurnID:     input.ParentTurnID,
		ParentAgentRunID: cloneAgentRunID(input.ParentAgentRunID),
		ParentToolCallID: input.ParentToolCallID,
		RequestID:        input.RequestID,
		Name:             input.Name,
		Definition:       input.Definition,
		Depth:            input.Depth,
		StartedAt:        startedAt,
		events: newAgentRunEventBuffer(
			r.limits.MaxEventCount,
			r.limits.MaxEventBytes,
		),
		cancel: cancel,
		done:   make(chan struct{}),
		state:  AgentRunStateRunning,
	}
	if len(persist) > 0 && persist[0] != nil {
		if err := persist[0](run); err != nil {
			cancel()

			return nil, nil, ctxerrors.Wrap(err, "persist agent run")
		}
	}

	r.runs[run.ID] = run

	return run, runCtx, nil
}

func cloneAgentRunID(value *uuid.UUID) *uuid.UUID {
	if value == nil {
		return nil
	}

	agentRunID := *value

	return &agentRunID
}

func (r *AgentRunRegistry) runningCountLocked() int {
	count := 0

	for _, run := range r.runs {
		if run.Snapshot().State == AgentRunStateRunning {
			count++
		}
	}

	return count
}

// Get looks up one run by ID within this session's registry.
func (r *AgentRunRegistry) Get(id uuid.UUID) (*AgentRun, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	run, ok := r.runs[id]

	return run, ok
}

// List returns every run this registry has ever started, in no particular
// order.
func (r *AgentRunRegistry) List() []*AgentRun {
	r.mu.RLock()
	defer r.mu.RUnlock()

	runs := make([]*AgentRun, 0, len(r.runs))
	for _, run := range r.runs {
		runs = append(runs, run)
	}

	return runs
}

// Cancel signals one run to stop without affecting any other run or the
// parent turn. Idempotent: an unknown, already cancelled, or already
// completed run reports its current state rather than failing. found is
// false only when id names no run this registry has ever started.
func (r *AgentRunRegistry) Cancel(id uuid.UUID) (AgentRunSnapshot, bool) {
	run, ok := r.Get(id)
	if !ok {
		return AgentRunSnapshot{}, false
	}

	snapshot := run.Snapshot()
	if snapshot.State != AgentRunStateRunning {
		return snapshot, true
	}

	run.cancel()

	return run.Snapshot(), true
}

// Finish records one run's terminal state and publishes its completion event
// exactly once. ctx is used only for logging a publish failure; the write to
// the bus never fails the caller.
func (r *AgentRunRegistry) Finish(
	ctx context.Context,
	run *AgentRun,
	state AgentRunState,
) {
	run.mu.Lock()
	run.state = state
	run.endedAt = time.Now().UTC()
	endedAt := run.endedAt
	run.mu.Unlock()

	close(run.done)

	r.publish(ctx, run, state, endedAt)
}

// publish announces one run's completion on the event bus. Summary carries
// the agent name and run ID rather than its task or response content,
// matching the never-log-agent-content discipline this package applies
// elsewhere.
func (r *AgentRunRegistry) publish(
	ctx context.Context,
	run *AgentRun,
	state AgentRunState,
	endedAt time.Time,
) {
	if r.publisher == nil {
		return
	}

	eventType, summary := agentCompletionEvent(run, state)

	data, err := json.Marshal(agentEventData{
		RunID:      run.ID,
		AgentName:  run.Name,
		DurationMs: endedAt.Sub(run.StartedAt).Milliseconds(),
	})
	if err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"encode agent completion event data",
			"err", err,
			"run_id", run.ID,
		)

		data = nil
	}

	notice := events.Notice{
		SessionID: r.sessionID,
		Type:      eventType,
		Source:    agentEventSource,
		Summary:   summary,
		Data:      data,
	}

	if _, err := r.publisher.PublishContext(ctx, notice); err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"publish agent completion event",
			"err", err,
			"run_id", run.ID,
		)
	}
}

// agentEventData is the structured payload carried by every agent.* notice.
type agentEventData struct {
	RunID      uuid.UUID `json:"runId"`
	AgentName  string    `json:"agentName"`
	DurationMs int64     `json:"durationMs"`
}

// agentCompletionEvent maps a terminal run state onto its event type and a
// one-line, model-readable summary. Only AgentRunStateCompleted maps to
// events.TypeAgentFinished; every other terminal state, including
// cancellation, is reported as events.TypeAgentFailed since the event
// package defines no third type.
func agentCompletionEvent(
	run *AgentRun,
	state AgentRunState,
) (events.Type, string) {
	if state == AgentRunStateCompleted {
		return events.TypeAgentFinished, fmt.Sprintf(
			"agent %q run %s finished",
			run.Name, run.ID,
		)
	}

	return events.TypeAgentFailed, fmt.Sprintf(
		"agent %q run %s did not finish (%s)",
		run.Name, run.ID, state,
	)
}
