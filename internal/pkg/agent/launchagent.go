package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/tools"
)

const (
	agentNameSeparator            = ", "
	agentsNoneMessage             = "no agents are available"
	childPromptAdditionalCapacity = 2
)

// agentDepthKey is the unexported context key carrying how many launch_agent
// calls already sit above the conversation now running. The root turn is
// depth 0; hostToolHandler forwards ctx unchanged to every tool Handler, and
// launch_agent's own handler sets a fresh depth on the context it hands its
// child's Run call, so the value is always the depth of whichever
// conversation is currently executing.
type agentDepthKey struct{}

func contextWithAgentDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, agentDepthKey{}, depth)
}

func agentDepthFromContext(ctx context.Context) int {
	depth, _ := ctx.Value(agentDepthKey{}).(int)

	return depth
}

// parentToolCallIDKey is the unexported context key carrying the elelem call
// ID of whichever tool call is currently executing. hostToolHandler sets it
// for every host tool call; launch_agent reads it back to record which call
// started a given agent run, for internal transcript correlation.
type parentToolCallIDKey struct{}

func contextWithParentToolCallID(
	ctx context.Context,
	callID string,
) context.Context {
	return context.WithValue(ctx, parentToolCallIDKey{}, callID)
}

func parentToolCallIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(parentToolCallIDKey{}).(string)

	return id
}

// launchAgentInput selects the child to run and its task. Exactly one of
// Agent and AgentDefinition must be set.
type launchAgentInput struct {
	Task            string                `json:"task"`
	Agent           string                `json:"agent"`
	AgentDefinition *agentDefinitionInput `json:"agentDefinition"`
}

// agentDefinitionInput is an inline, never-persisted child definition.
type agentDefinitionInput struct {
	Name         string `json:"name"`
	Instructions string `json:"instructions"`
}

// launchAgentOutput is the child's final answer, or a typed cancellation
// result when this specific run, rather than the parent turn, was
// cancelled.
type launchAgentOutput struct {
	AgentRunID uuid.UUID          `json:"agentRunId"`
	Name       string             `json:"name"`
	Definition AgentRunDefinition `json:"definition"`
	Cancelled  bool               `json:"cancelled"`
	Response   string             `json:"response,omitempty"`
}

// childDefinition is the resolved child identity and instructions, whether
// they came from a stored agent file or an ad-hoc definition.
type childDefinition struct {
	name         string
	instructions string
	allowedTools []string
	kind         AgentRunDefinition
}

// launchAgentDeps is everything one turn's launch_agent tool needs to run a
// child conversation sharing this turn's session, workspace, resolved
// rules, tool registry, and model. Built once per turn in
// Runtime.runProvider and reused unchanged at every recursion depth: only
// the context's depth value and the run's own identity change as
// launch_agent calls itself for a grandchild.
type launchAgentDeps struct {
	runtime        *Runtime
	executor       *tools.JobExecutor
	snapshot       harness.Snapshot
	model          ModelClient
	modelReference string
	sessionID      uuid.UUID
	parentTurnID   uuid.UUID
}

// handle is the tool Handler launch_agent registers.
func (d *launchAgentDeps) handle(
	ctx context.Context,
	input launchAgentInput,
) (launchAgentOutput, error) {
	return d.runtime.launchAgent(ctx, d, input)
}

// launchAgentTool builds the launch_agent tool over one turn's (or one
// child's) dependencies. It is added to a *elelem.ToolSet that hostToolSet
// already built, via ToolSet.Add, rather than being part of hostToolSet
// itself: hostToolSet has other callers (see skills_test.go, tools_test.go)
// that have no launchAgentDeps to give it.
func launchAgentTool(
	deps *launchAgentDeps,
	onPostRun elelem.MessageInjector,
) elelem.Tool {
	return hostTool(
		toolNameLaunchAgent,
		launchAgentDescription,
		launchAgentSchema,
		deps.handle,
		onPostRun,
	)
}

// launchAgent validates the requested child, enforces depth and turn limits
// before launching, and runs it synchronously to completion.
func (r *Runtime) launchAgent(
	ctx context.Context,
	deps *launchAgentDeps,
	input launchAgentInput,
) (launchAgentOutput, error) {
	if strings.TrimSpace(input.Task) == "" {
		return launchAgentOutput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"task is required",
		)
	}

	definition, err := resolveChildDefinition(
		deps.snapshot,
		input,
		r.agentLimits.MaxAdHocInstructionBytes,
	)
	if err != nil {
		return launchAgentOutput{}, err
	}

	depth := agentDepthFromContext(ctx) + 1
	if depth > r.agentLimits.MaxDepth {
		return launchAgentOutput{}, ctxerrors.Wrapf(
			ErrAgentDepthExceeded,
			"child agent depth would reach %d, limit is %d",
			depth, r.agentLimits.MaxDepth,
		)
	}

	registry, err := r.sessionAgentRuns(deps.sessionID)
	if err != nil {
		return launchAgentOutput{}, err
	}

	run, runCtx, err := registry.Start(ctx, StartAgentRunInput{
		ParentTurnID:     deps.parentTurnID,
		ParentToolCallID: parentToolCallIDFromContext(ctx),
		Name:             definition.name,
		Definition:       definition.kind,
		Depth:            depth,
	})
	if err != nil {
		return launchAgentOutput{}, err
	}

	runCtx = childAgentContext(runCtx, run, definition, depth, input.Task)

	response, runErr := r.runChildAgent(
		runCtx,
		deps,
		definition,
		input.Task,
		run,
		depth,
	)

	return r.finishAgentRun(ctx, registry, run, response, runErr)
}

func childAgentContext(
	ctx context.Context,
	run *AgentRun,
	definition childDefinition,
	depth int,
	task string,
) context.Context {
	childCtx := ctxscope.Set(
		ctx,
		ctxscope.Attr("agent_run_id", run.ID.String()),
		ctxscope.Attr("agent_name", run.Name),
		ctxscope.Attr("agent_depth", depth),
	)
	ctxscope.GetLogger(childCtx).Info(
		"child agent started",
		"agent_definition", definition.kind,
		"task_bytes", len(task),
		"task_sha256", hash(task),
	)

	return childCtx
}

// resolveChildDefinition validates that exactly one of Agent and
// AgentDefinition was supplied and resolves it into a childDefinition.
func resolveChildDefinition(
	snapshot harness.Snapshot,
	input launchAgentInput,
	maxAdHocBytes int,
) (childDefinition, error) {
	hasStored := strings.TrimSpace(input.Agent) != ""
	hasAdHoc := input.AgentDefinition != nil

	if hasStored == hasAdHoc {
		return childDefinition{}, ctxerrors.Wrap(
			ErrAgentSpecificationInvalid,
			"supply exactly one of agent or agentDefinition",
		)
	}

	if hasStored {
		return storedChildDefinition(snapshot, input.Agent)
	}

	return adHocChildDefinition(*input.AgentDefinition, maxAdHocBytes)
}

func storedChildDefinition(
	snapshot harness.Snapshot,
	name string,
) (childDefinition, error) {
	found, err := snapshot.Agent(name)
	if err != nil {
		return childDefinition{}, unknownAgentError(snapshot, name, err)
	}

	return childDefinition{
		name:         found.Name,
		instructions: found.Instructions,
		allowedTools: found.AllowedTools,
		kind:         AgentRunDefinitionStored,
	}, nil
}

func adHocChildDefinition(
	definition agentDefinitionInput,
	maxBytes int,
) (childDefinition, error) {
	name := strings.TrimSpace(definition.Name)
	instructions := definition.Instructions

	if name == "" || strings.TrimSpace(instructions) == "" {
		return childDefinition{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"ad-hoc agent definition requires a name and instructions",
		)
	}

	if maxBytes > 0 && len(instructions) > maxBytes {
		return childDefinition{}, ctxerrors.Wrapf(
			ErrAgentDefinitionTooLarge,
			"ad-hoc instructions of %d bytes exceed the %d byte bound",
			len(instructions), maxBytes,
		)
	}

	return childDefinition{
		name:         name,
		instructions: instructions,
		kind:         AgentRunDefinitionAdHoc,
	}, nil
}

// unknownAgentError names the currently effective agents so the model can
// correct its own call instead of the turn ending on a bad name.
func unknownAgentError(
	snapshot harness.Snapshot,
	name string,
	cause error,
) error {
	available := agentsNoneMessage

	names := agentNames(snapshot)
	if len(names) > 0 {
		available = "available agents: " +
			strings.Join(names, agentNameSeparator)
	}

	return ctxerrors.Wrapf(cause, "agent %q not found, %s", name, available)
}

func agentNames(snapshot harness.Snapshot) []string {
	agents := snapshot.Agents()

	names := make([]string, 0, len(agents))
	for _, found := range agents {
		names = append(names, found.Name)
	}

	return names
}

// childSystemPrompt assembles the child's system prompt from the same
// resolved rules and skill catalogue the parent turn sees, with the child's
// own instructions (from a stored agent file or an ad-hoc definition) in
// place of the parent's root agent block.
func (r *Runtime) childSystemPrompt(
	snapshot harness.Snapshot,
	childInstructions string,
	workspace string,
) (string, error) {
	blocks, err := snapshot.PromptBlocks("")
	if err != nil {
		return "", ctxerrors.Wrap(err, "resolve child prompt blocks")
	}

	sections := make(
		[]string,
		0,
		len(blocks)+childPromptAdditionalCapacity,
	)
	for _, block := range blocks {
		sections = append(sections, block.Content)
	}

	workspaceBlock, err := workspaceMetadataBlock(workspace)
	if err != nil {
		return "", err
	}

	sections = append(
		sections,
		childInstructions,
		workspaceBlock,
		r.currentTimeBlock(),
	)

	return strings.Join(sections, systemSectionGap), nil
}

// runChildAgent builds and runs the child's own, independent conversation:
// its own system prompt, its own tool set built over the SAME shared tool
// registry and resolved rules, and its own event sink feeding this run's
// ring buffer and JSONL mirror. The child gets no session-event injection;
// per the plan, a child agent has no private notification path.
//
//nolint:funlen // Keep child request lifecycle together.
func (r *Runtime) runChildAgent(
	runCtx context.Context,
	deps *launchAgentDeps,
	definition childDefinition,
	task string,
	run *AgentRun,
	depth int,
) (*elelem.Response, error) {
	transcript := newAgentRunTranscript(
		runCtx,
		r.configDirectory,
		deps.sessionID,
		definition.name,
		run.ID,
	)

	systemPrompt, err := r.childSystemPrompt(
		deps.snapshot,
		definition.instructions,
		deps.executor.Workspace(),
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build child system prompt")
	}

	childToolSet, err := r.agentToolSet(
		deps,
		nil,
		definition.allowedTools,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build child agent tool set")
	}

	childHooks, err := newToolHookRuntime(
		deps.snapshot,
		deps.executor.Workspace(),
		uuid.Nil,
		deps.sessionID,
		deps.parentTurnID,
		deps.executor,
		nil,
		r.enableWorkspaceHooks,
		r.hookCommandTimeout,
		r.maxHookCommandOutput,
		r.eventBus,
		nil,
		modelTokenCounter(deps.model),
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create child tool hooks")
	}

	bindToolHooks(childToolSet, childHooks)

	sink := newAgentRunSink(run, transcript)
	depthCtx := contextWithAgentDepth(runCtx, depth)
	startedAt := time.Now()

	var (
		firstDeltaAt   time.Time
		firstDeltaOnce sync.Once
	)

	response, err := elelem.NewRequest(deps.model.Client).
		WithModel(deps.model.Model).
		WithPrompt(elelem.NewPrompt().WithSystem(systemPrompt).UserText(task)).
		WithTools(childToolSet).
		WithAutoToolCalls().
		WithMaxRounds(r.agentLimits.MaxChildTurns).
		WithMaxConcurrentTools(r.maxConcurrentTools).
		WithToolTimeout(r.toolTimeout).
		WithMaxToolResultTokens(r.maxToolResultTokens).
		WithMaxContextTokens(r.maxContextTokens).
		OnText(sink.onText).
		OnReasoning(sink.onReasoning).
		OnToolCallStart(sink.onToolCallStart).
		OnToolResult(sink.onToolResult).
		OnDelta(func(_ context.Context, _ elelem.Delta) error {
			firstDeltaOnce.Do(func() { firstDeltaAt = time.Now() })

			return nil
		}).
		Run(depthCtx)

	observeModelRequest(
		r.metrics,
		modelMetricStageChild,
		modelMetricFunctionRunChild,
		deps.modelReference,
		startedAt,
		firstDeltaAt,
		response,
		err,
	)

	if err != nil {
		return nil, ctxerrors.Wrap(err, "run child agent request")
	}

	return response, nil
}

func (r *Runtime) agentToolSet(
	deps *launchAgentDeps,
	onPostRun elelem.MessageInjector,
	allowedTools []string,
) (*elelem.ToolSet, error) {
	toolSet := hostToolSet(
		deps.executor,
		onPostRun,
		deps.snapshot,
		r.metrics,
	)
	toolSet.Add(instrumentTool(launchAgentTool(deps, onPostRun), r.metrics))

	restricted, err := restrictToolSet(toolSet, allowedTools)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "restrict agent tools")
	}

	return restricted, nil
}

// finishAgentRun records the run's terminal state and shapes the tool
// result the parent sees.
//
// A cancellation is reported the same way regardless of source, but only
// propagates as a real error, ending the parent turn, when parentCtx itself
// is also done: that is what tells the two cases apart. When only the run's
// own dedicated cancellation fired (a single-run Cancel call), parentCtx is
// still alive, and the call returns a typed, model-visible success instead
// of failing the turn.
func (r *Runtime) finishAgentRun(
	parentCtx context.Context,
	registry *AgentRunRegistry,
	run *AgentRun,
	response *elelem.Response,
	runErr error,
) (launchAgentOutput, error) {
	if runErr != nil {
		return r.finishFailedAgentRun(parentCtx, registry, run, runErr)
	}

	registry.Finish(parentCtx, run, AgentRunStateCompleted)
	ctxscope.GetLogger(parentCtx).Info(
		"child agent completed",
		"agent_run_id", run.ID.String(),
		"agent_name", run.Name,
		"response_bytes", len(response.Text),
		"response_sha256", hash(response.Text),
	)

	return launchAgentOutput{
		AgentRunID: run.ID,
		Name:       run.Name,
		Definition: run.Definition,
		Response:   response.Text,
	}, nil
}

func (r *Runtime) finishFailedAgentRun(
	parentCtx context.Context,
	registry *AgentRunRegistry,
	run *AgentRun,
	runErr error,
) (launchAgentOutput, error) {
	if !errors.Is(runErr, context.Canceled) {
		registry.Finish(parentCtx, run, AgentRunStateFailed)
		ctxscope.GetLogger(parentCtx).Warn(
			"child agent failed",
			"agent_run_id", run.ID.String(),
			"agent_name", run.Name,
			"err", runErr,
		)

		return launchAgentOutput{}, ctxerrors.Wrap(runErr, "run child agent")
	}

	registry.Finish(parentCtx, run, AgentRunStateCancelled)
	ctxscope.GetLogger(parentCtx).Info(
		"child agent cancelled",
		"agent_run_id", run.ID.String(),
		"agent_name", run.Name,
	)

	if parentCtx.Err() != nil {
		return launchAgentOutput{}, ctxerrors.Wrap(runErr, "run child agent")
	}

	return launchAgentOutput{
		AgentRunID: run.ID,
		Name:       run.Name,
		Definition: run.Definition,
		Cancelled:  true,
	}, nil
}

// sessionAgentRuns returns the session's agent run registry, creating it on
// first use, mirroring Runtime.sessionJobs.
func (r *Runtime) sessionAgentRuns(
	sessionID uuid.UUID,
) (*AgentRunRegistry, error) {
	r.agentRunsMutex.Lock()
	defer r.agentRunsMutex.Unlock()

	if registry, ok := r.agentRuns[sessionID]; ok {
		return registry, nil
	}

	registry, err := NewAgentRunRegistry(sessionID, r.eventBus, r.agentLimits)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create session agent run registry")
	}

	r.agentRuns[sessionID] = registry

	return registry, nil
}

// agentRunSink mirrors one child conversation's own event stream into its
// run's bounded ring buffer and JSONL transcript, using the same event type
// and payload shapes the top-level turn's own tool blocks use. It never
// touches the parent turn's own transcript: a child agent has no private
// notification path into the session, and its record lives entirely under
// its own run.
type agentRunSink struct {
	run        *AgentRun
	transcript *agentRunTranscript
}

func newAgentRunSink(
	run *AgentRun,
	transcript *agentRunTranscript,
) *agentRunSink {
	return &agentRunSink{run: run, transcript: transcript}
}

func (s *agentRunSink) emit(
	ctx context.Context,
	eventType string,
	payload any,
) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ctxerrors.Wrap(err, "marshal agent run event")
	}

	event := s.run.events.Append(eventType, encoded)

	s.transcript.Append(ctx, transcriptLine{
		Sequence:         event.Sequence,
		RunID:            s.run.ID,
		ParentToolCallID: s.run.ParentToolCallID,
		Type:             eventType,
		Payload:          encoded,
		CreatedAt:        event.CreatedAt,
	})

	return nil
}

func (s *agentRunSink) onText(
	ctx context.Context,
	delta elelem.TextDelta,
) error {
	return s.emit(ctx, EventTypeTextDelta, textDeltaPayload{Text: delta.Text})
}

func (s *agentRunSink) onReasoning(
	ctx context.Context,
	delta elelem.ReasoningDelta,
) error {
	return s.emit(
		ctx,
		EventTypeThinkingDelta,
		textDeltaPayload{Text: delta.Text},
	)
}

func (s *agentRunSink) onToolCallStart(
	ctx context.Context,
	call elelem.ToolCallEvent,
) error {
	return s.emit(ctx, EventTypeToolUse, toolUsePayload{
		CallID:    call.CallID,
		Name:      call.Name,
		Arguments: call.Arguments,
	})
}

func (s *agentRunSink) onToolResult(
	ctx context.Context,
	call elelem.ToolCallEvent,
) error {
	content, isError := "", false
	if call.Result != nil {
		content = call.Result.Content
		isError = call.Result.IsError
	}

	return s.emit(ctx, EventTypeToolResult, toolResultPayload{
		CallID:  call.CallID,
		Name:    call.Name,
		Content: content,
		IsError: isError,
	})
}
