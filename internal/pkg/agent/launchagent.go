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
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
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

// parentAgentRunIDKey carries the direct child-run parent while a nested
// launch_agent call executes. Root calls have no parent agent run.
type parentAgentRunIDKey struct{}

func contextWithParentAgentRunID(
	ctx context.Context,
	agentRunID uuid.UUID,
) context.Context {
	return context.WithValue(ctx, parentAgentRunIDKey{}, agentRunID)
}

func parentAgentRunIDFromContext(ctx context.Context) *uuid.UUID {
	agentRunID, ok := ctx.Value(parentAgentRunIDKey{}).(uuid.UUID)
	if !ok || agentRunID == uuid.Nil {
		return nil
	}

	return &agentRunID
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

type preparedChildLaunch struct {
	definition       childDefinition
	depth            int
	systemPrompt     string
	allowedToolsJSON string
	registry         *AgentRunRegistry
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
	requestID      uuid.UUID
	liveSink       EventSink
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

	prepared, err := r.prepareChildLaunch(ctx, deps, input)
	if err != nil {
		return launchAgentOutput{}, err
	}

	return r.executeChildLaunch(ctx, deps, input, prepared)
}

func (r *Runtime) prepareChildLaunch(
	ctx context.Context,
	deps *launchAgentDeps,
	input launchAgentInput,
) (preparedChildLaunch, error) {
	definition, err := resolveChildDefinition(
		deps.snapshot,
		input,
		r.agentLimits.MaxAdHocInstructionBytes,
	)
	if err != nil {
		return preparedChildLaunch{}, err
	}

	depth := agentDepthFromContext(ctx) + 1
	if depth > r.agentLimits.MaxDepth {
		return preparedChildLaunch{}, ctxerrors.Wrapf(
			ErrAgentDepthExceeded,
			"child agent depth would reach %d, limit is %d",
			depth, r.agentLimits.MaxDepth,
		)
	}

	systemPrompt, err := r.childSystemPrompt(
		deps.snapshot,
		definition.instructions,
		deps.executor.Workspace(),
	)
	if err != nil {
		return preparedChildLaunch{}, ctxerrors.Wrap(
			err,
			"build child system prompt",
		)
	}

	allowedToolsJSON, err := marshalAllowedTools(definition.allowedTools)
	if err != nil {
		return preparedChildLaunch{}, err
	}

	registry, err := r.sessionAgentRuns(deps.sessionID)
	if err != nil {
		return preparedChildLaunch{}, err
	}

	return preparedChildLaunch{
		definition:       definition,
		depth:            depth,
		systemPrompt:     systemPrompt,
		allowedToolsJSON: allowedToolsJSON,
		registry:         registry,
	}, nil
}

func (r *Runtime) executeChildLaunch(
	ctx context.Context,
	deps *launchAgentDeps,
	input launchAgentInput,
	prepared preparedChildLaunch,
) (launchAgentOutput, error) {
	run, runCtx, err := r.startChildAgentRun(ctx, deps, input, prepared)
	if err != nil {
		return launchAgentOutput{}, err
	}

	runCtx = childAgentContext(
		runCtx,
		run,
		prepared.definition,
		prepared.depth,
		input.Task,
	)

	sink := newAgentRunSink(r.store, deps.sessionID, run, deps.liveSink)
	if err := sink.emit(
		runCtx,
		EventTypeAgentRunStarted,
		agentRunStartedPayload{
			AgentRunID:       run.ID,
			ParentTurnID:     run.ParentTurnID,
			ParentAgentRunID: run.ParentAgentRunID,
			Name:             run.Name,
			Definition:       run.Definition,
			Depth:            run.Depth,
			Model:            deps.modelReference,
			Workspace:        deps.executor.Workspace(),
		},
	); err != nil {
		return r.finishAgentRun(ctx, prepared.registry, sink, run, nil, err)
	}

	response, runErr := r.runChildAgent(
		runCtx,
		deps,
		prepared.definition,
		input.Task,
		run,
		prepared.depth,
		prepared.systemPrompt,
		sink,
	)

	return r.finishAgentRun(ctx, prepared.registry, sink, run, response, runErr)
}

func (r *Runtime) startChildAgentRun(
	ctx context.Context,
	deps *launchAgentDeps,
	input launchAgentInput,
	prepared preparedChildLaunch,
) (*AgentRun, context.Context, error) {
	return prepared.registry.Start(ctx, StartAgentRunInput{
		ID:               uuid.New(),
		ParentTurnID:     deps.parentTurnID,
		ParentAgentRunID: parentAgentRunIDFromContext(ctx),
		ParentToolCallID: parentToolCallIDFromContext(ctx),
		RequestID:        deps.requestID,
		Name:             prepared.definition.name,
		Definition:       prepared.definition.kind,
		Depth:            prepared.depth,
		StartedAt:        r.now().UTC(),
	}, func(run *AgentRun) error {
		return r.createChildAgentRun(ctx, deps, input, prepared, run)
	})
}

func (r *Runtime) createChildAgentRun(
	ctx context.Context,
	deps *launchAgentDeps,
	input launchAgentInput,
	prepared preparedChildLaunch,
	run *AgentRun,
) error {
	_, err := r.store.CreateAgentRun(
		ctx,
		deps.sessionID,
		session.StartAgentRunInput{
			ID:               run.ID,
			ParentTurnID:     run.ParentTurnID,
			ParentAgentRunID: run.ParentAgentRunID,
			ParentToolCallID: run.ParentToolCallID,
			RequestID:        run.RequestID,
			Name:             run.Name,
			Definition:       models.AgentRunDefinition(run.Definition),
			Depth:            int64(run.Depth),
			Workspace:        deps.executor.Workspace(),
			ModelReference:   deps.modelReference,
			ModelID:          deps.model.Model.ID,
			Task:             input.Task,
			Instructions:     prepared.definition.instructions,
			AllowedToolsJSON: prepared.allowedToolsJSON,
			SystemPrompt:     prepared.systemPrompt,
			StartedAt:        run.StartedAt,
		},
	)

	return ctxerrors.Wrap(err, "create durable child agent")
}

func marshalAllowedTools(allowedTools []string) (string, error) {
	if allowedTools == nil {
		allowedTools = []string{}
	}

	encoded, err := json.Marshal(allowedTools)
	if err != nil {
		return "", ctxerrors.Wrap(err, "marshal child allowed tools")
	}

	return string(encoded), nil
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
	childCtx = contextWithParentAgentRunID(childCtx, run.ID)
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

// runChildAgent builds and runs one independent child conversation. Its event
// sink persists every observable callback before publishing the live tail.
//
//nolint:funlen // Keep child request lifecycle together.
func (r *Runtime) runChildAgent(
	runCtx context.Context,
	deps *launchAgentDeps,
	definition childDefinition,
	task string,
	run *AgentRun,
	depth int,
	systemPrompt string,
	sink *agentRunSink,
) (*elelem.Response, error) {
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
		r.durableEventPublisher(),
		nil,
		modelTokenCounter(deps.model),
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create child tool hooks")
	}

	bindToolHooks(childToolSet, childHooks)

	requestSettingsJSON, err := newModelAuditSettings(
		deps.model.Model,
		true,
		r.agentLimits.MaxChildTurns,
		r.maxContextTokens,
		0,
		r.maxConcurrentTools,
		r.toolTimeout,
		r.maxToolResultTokens,
		0,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "marshal child model request settings")
	}

	agentRunID := run.ID

	audit, err := newModelAuditRecorder(runCtx, modelAuditOptions{
		Store:               r.store,
		SessionID:           deps.sessionID,
		TurnID:              deps.parentTurnID,
		AgentRunID:          &agentRunID,
		Stage:               models.ModelRunStageChild,
		ModelReference:      deps.modelReference,
		Model:               deps.model.Model,
		RequestSettingsJSON: requestSettingsJSON,
		Now:                 r.now,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "start child model audit")
	}

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
		OnRoundStart(audit.onRoundStart).
		OnRoundEnd(audit.onRoundEnd).
		OnAssistantMessage(func(
			callbackCtx context.Context,
			message elelem.Message,
		) error {
			if err := sink.onAssistantMessage(
				callbackCtx,
				message,
			); err != nil {
				return ctxerrors.Wrap(err, "persist child assistant message")
			}

			return audit.onAssistantMessage(callbackCtx, message)
		}).
		OnToolCallStart(sink.onToolCallStart).
		OnToolResult(sink.onToolResult).
		OnMessageInjection(sink.onMessageInjection).
		OnRetry(func(
			callbackCtx context.Context,
			attempt elelem.RetryAttempt,
		) error {
			if err := sink.onRetry(callbackCtx, attempt); err != nil {
				return ctxerrors.Wrap(err, "persist child provider retry")
			}

			return audit.onRetry(callbackCtx, attempt)
		}).
		OnDelta(func(_ context.Context, _ elelem.Delta) error {
			firstDeltaOnce.Do(func() { firstDeltaAt = time.Now() })

			return nil
		}).
		Run(depthCtx)

	persistCtx := context.WithoutCancel(depthCtx)
	if auditErr := audit.finish(persistCtx, response, err); auditErr != nil {
		err = errors.Join(err, auditErr)
	}

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
		return response, ctxerrors.Wrap(err, "run child agent request")
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
	sink *agentRunSink,
	run *AgentRun,
	response *elelem.Response,
	runErr error,
) (launchAgentOutput, error) {
	state, classification, eventType := agentRunOutcome(runErr)
	if runErr == nil && response == nil {
		runErr = ctxerrors.New("child provider returned no response")
		state, classification, eventType = agentRunOutcome(runErr)
	}

	if err := r.persistAgentRunOutcome(
		parentCtx,
		registry,
		sink,
		run,
		response,
		runErr,
		state,
		classification,
		eventType,
	); err != nil {
		return launchAgentOutput{}, err
	}

	return childAgentResult(parentCtx, run, response, runErr, state)
}

func (r *Runtime) persistAgentRunOutcome(
	parentCtx context.Context,
	registry *AgentRunRegistry,
	sink *agentRunSink,
	run *AgentRun,
	response *elelem.Response,
	runErr error,
	state AgentRunState,
	classification string,
	eventType string,
) error {
	if err := r.finalizeAgentRun(
		context.WithoutCancel(parentCtx),
		run,
		response,
		state,
		classification,
		runErr,
	); err != nil {
		registry.Finish(parentCtx, run, AgentRunStateFailed)

		return ctxerrors.Wrap(err, "persist child agent outcome")
	}

	registry.Finish(parentCtx, run, state)

	persistCtx := context.WithoutCancel(parentCtx)
	if err := sink.emit(persistCtx, eventType, agentRunTerminalPayload{
		AgentRunID:            run.ID,
		State:                 state,
		FailureClassification: classification,
		FailureDetail:         errorText(runErr),
	}); err != nil {
		return ctxerrors.Wrap(err, "persist child agent terminal event")
	}

	return nil
}

func childAgentResult(
	parentCtx context.Context,
	run *AgentRun,
	response *elelem.Response,
	runErr error,
	state AgentRunState,
) (launchAgentOutput, error) {
	if runErr != nil {
		if state == AgentRunStateCancelled && parentCtx.Err() == nil {
			ctxscope.GetLogger(parentCtx).Info(
				"child agent cancelled",
				"agent_run_id", run.ID.String(),
				"agent_name", run.Name,
			)

			return launchAgentOutput{
				AgentRunID: run.ID,
				Name:       run.Name,
				Definition: run.Definition,
				Cancelled:  true,
			}, nil
		}

		ctxscope.GetLogger(parentCtx).Warn(
			"child agent failed",
			"agent_run_id", run.ID.String(),
			"agent_name", run.Name,
			"state", state,
			"err", runErr,
		)

		return launchAgentOutput{}, ctxerrors.Wrap(runErr, "run child agent")
	}

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

func (r *Runtime) finalizeAgentRun(
	ctx context.Context,
	run *AgentRun,
	response *elelem.Response,
	state AgentRunState,
	classification string,
	runErr error,
) error {
	input := session.FinalizeAgentRunInput{
		State:                 models.AgentRunState(state),
		FailureClassification: classification,
		FailureDetail:         errorText(runErr),
	}

	if response != nil {
		messages, err := json.Marshal(response.Messages)
		if err != nil {
			return ctxerrors.Wrap(err, "marshal child response messages")
		}

		input.ResponseText = response.Text
		input.ResponseThinking = response.Reasoning
		input.ResponseMessagesJSON = string(messages)
		input.FinishReason = string(response.FinishReason)
		input.PromptTokenCount = response.Usage.Prompt
		input.CompletionTokenCount = response.Usage.Completion
	}

	if _, err := r.store.FinalizeAgentRun(
		ctx,
		run.SessionID,
		run.ID,
		input,
	); err != nil {
		return ctxerrors.Wrap(err, "finalize durable child agent")
	}

	return nil
}

func agentRunOutcome(runErr error) (AgentRunState, string, string) {
	if runErr == nil {
		return AgentRunStateCompleted, "", EventTypeAgentRunCompleted
	}

	if errors.Is(runErr, context.Canceled) {
		return AgentRunStateCancelled,
			failureClassCancelled,
			EventTypeAgentRunCancelled
	}

	return AgentRunStateFailed, failureClassAgentRun, EventTypeAgentRunFailed
}

func errorText(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
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

	registry, err := NewAgentRunRegistry(
		sessionID,
		r.durableEventPublisher(),
		r.agentLimits,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create session agent run registry")
	}

	r.agentRuns[sessionID] = registry

	return registry, nil
}

// agentRunSink persists a child stream, refreshes the local convenience
// buffer, then emits the exact child event to every live session client.
type agentRunSink struct {
	store     *session.Store
	sessionID uuid.UUID
	run       *AgentRun
	liveSink  EventSink
	mu        sync.Mutex
}

func newAgentRunSink(
	store *session.Store,
	sessionID uuid.UUID,
	run *AgentRun,
	liveSink EventSink,
) *agentRunSink {
	return &agentRunSink{
		store:     store,
		sessionID: sessionID,
		run:       run,
		liveSink:  liveSink,
	}
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

	s.mu.Lock()
	defer s.mu.Unlock()

	persisted, err := s.store.AppendAgentRunEvent(
		context.WithoutCancel(ctx),
		s.sessionID,
		s.run.ID,
		session.AgentRunEventInput{
			EventType:   eventType,
			PayloadJSON: string(encoded),
		},
	)
	if err != nil {
		return ctxerrors.Wrap(err, "persist agent run event")
	}

	s.run.AppendStoredEvent(AgentRunEvent{
		Sequence:  int(persisted.Sequence),
		Type:      persisted.EventType,
		Payload:   json.RawMessage(persisted.PayloadJSON),
		CreatedAt: persisted.CreatedAt,
	})

	if s.liveSink == nil {
		return nil
	}

	livePayload, err := json.Marshal(agentRunLivePayload{
		AgentRunID:       s.run.ID,
		ParentTurnID:     s.run.ParentTurnID,
		ParentAgentRunID: s.run.ParentAgentRunID,
		Event:            json.RawMessage(persisted.PayloadJSON),
	})
	if err != nil {
		return ctxerrors.Wrap(err, "marshal live agent run event")
	}

	if err := s.liveSink(Event{
		Type:    eventType,
		Payload: livePayload,
	}); err != nil {
		return ctxerrors.Wrap(err, "publish live agent run event")
	}

	return nil
}

func (s *agentRunSink) onText(
	ctx context.Context,
	delta elelem.TextDelta,
) error {
	return s.emit(
		ctx,
		EventTypeAgentRunTextDelta,
		textDeltaPayload{Text: delta.Text},
	)
}

func (s *agentRunSink) onReasoning(
	ctx context.Context,
	delta elelem.ReasoningDelta,
) error {
	return s.emit(
		ctx,
		EventTypeAgentRunThinkingDelta,
		textDeltaPayload{Text: delta.Text},
	)
}

func (s *agentRunSink) onToolCallStart(
	ctx context.Context,
	call elelem.ToolCallEvent,
) error {
	return s.emit(ctx, EventTypeAgentRunToolUse, toolUsePayload{
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

	return s.emit(ctx, EventTypeAgentRunToolResult, toolResultPayload{
		CallID:  call.CallID,
		Name:    call.Name,
		Content: content,
		IsError: isError,
	})
}

func (s *agentRunSink) onAssistantMessage(
	ctx context.Context,
	message elelem.Message,
) error {
	return s.emit(ctx, EventTypeAgentRunAssistantMessage, message)
}

func (s *agentRunSink) onMessageInjection(
	ctx context.Context,
	injection elelem.MessageInjection,
) error {
	return s.emit(ctx, EventTypeAgentRunMessageInjected, injection)
}

func (s *agentRunSink) onRetry(
	ctx context.Context,
	attempt elelem.RetryAttempt,
) error {
	return s.emit(ctx, EventTypeAgentRunProviderRetry, providerRetryPayload{
		Attempt: attempt.Attempt,
		Reason:  attempt.Reason,
		Status:  attempt.Status,
		DelayMS: attempt.Delay.Milliseconds(),
	})
}

type agentRunStartedPayload struct {
	AgentRunID       uuid.UUID          `json:"agentRunId"`
	ParentTurnID     uuid.UUID          `json:"parentTurnId"`
	ParentAgentRunID *uuid.UUID         `json:"parentAgentRunId,omitempty"`
	Name             string             `json:"name"`
	Definition       AgentRunDefinition `json:"definition"`
	Depth            int                `json:"depth"`
	Model            string             `json:"model"`
	Workspace        string             `json:"workspace"`
}

type agentRunTerminalPayload struct {
	AgentRunID            uuid.UUID     `json:"agentRunId"`
	State                 AgentRunState `json:"state"`
	FailureClassification string        `json:"failureClassification,omitempty"`
	FailureDetail         string        `json:"failureDetail,omitempty"`
}

type agentRunLivePayload struct {
	AgentRunID       uuid.UUID       `json:"agentRunId"`
	ParentTurnID     uuid.UUID       `json:"parentTurnId"`
	ParentAgentRunID *uuid.UUID      `json:"parentAgentRunId,omitempty"`
	Event            json.RawMessage `json:"event"`
}
