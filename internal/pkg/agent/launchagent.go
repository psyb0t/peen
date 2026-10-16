package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"runtime/debug"
	"slices"
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
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/tools"
)

const (
	agentNameSeparator = ", "
	agentsNoneMessage  = "no agents are available"
	// childPromptAdditionalCapacity counts the sections a child prompt adds
	// around the resolved harness blocks: the base system prompt, the child's
	// own instructions, the workspace block, and the runtime block.
	childPromptAdditionalCapacity = 4
	rootAgentDepth                = 0

	// agentCancelledText and agentFailedLead are the body of the message a
	// nested child's parent receives when the child did not complete.
	agentCancelledText = "The agent was cancelled before it finished."
	agentFailedLead    = "The agent failed: "
)

// agentDepthKey is the unexported context key carrying how many launchAgent
// calls already sit above the conversation now running. The root turn is
// depth 0; hostToolHandler forwards ctx unchanged to every tool Handler, and
// launchAgent's own handler sets a fresh depth on the context it hands its
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
// for every host tool call; launchAgent reads it back to record which call
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

// parentAgentRunKey carries the run whose conversation is executing while a
// nested launchAgent call runs. Root calls have no parent agent run.
type parentAgentRunKey struct{}

// parentAgentRun is the identity a child's messages are tagged with.
type parentAgentRun struct {
	id   uuid.UUID
	name string
}

func contextWithParentAgentRun(
	ctx context.Context,
	run *AgentRun,
) context.Context {
	return context.WithValue(ctx, parentAgentRunKey{}, parentAgentRun{
		id:   run.ID,
		name: run.Name,
	})
}

func parentAgentRunFromContext(ctx context.Context) (parentAgentRun, bool) {
	parent, ok := ctx.Value(parentAgentRunKey{}).(parentAgentRun)
	if !ok || parent.id == uuid.Nil {
		return parentAgentRun{}, false
	}

	return parent, true
}

func parentAgentRunIDFromContext(ctx context.Context) *uuid.UUID {
	parent, ok := parentAgentRunFromContext(ctx)
	if !ok {
		return nil
	}

	return &parent.id
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

// launchAgentOutput names the child that is now running. Its answer arrives
// later, as a message to its parent. ParentAgentRunID is the parent's run ID,
// or "root" for the session's root conversation.
type launchAgentOutput struct {
	AgentRunID       uuid.UUID          `json:"agentRunId"`
	Name             string             `json:"name"`
	Definition       AgentRunDefinition `json:"definition"`
	ParentAgentRunID string             `json:"parentAgentRunId"`
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
	recoveredPrompt  *elelem.Prompt
	completedRounds  int
	maximumRounds    int
	definition       childDefinition
	depth            int
	systemPrompt     string
	allowedToolsJSON string
	registry         *AgentRunRegistry

	// skills are the messages that follow the task, and skillLedger records
	// every skill the child's conversation holds. The child's conversation
	// starts with none, whatever the parent has loaded.
	skills      []skillMessage
	skillLedger *skillLedger
}

// launchAgentDeps is everything one turn's launchAgent tool needs to run a
// child conversation sharing this turn's session, workspace, resolved
// rules, tool registry, and model. Built once per turn in
// Runtime.runProvider and reused unchanged at every recursion depth: only
// the context's depth value and the run's own identity change as
// launchAgent calls itself for a grandchild.
type launchAgentDeps struct {
	directoryScope     *directoryScope
	runtime            *Runtime
	executor           *tools.JobExecutor
	snapshot           harness.Snapshot
	model              ModelClient
	modelReference     string
	workerGenerationID string
	sessionID          uuid.UUID
	parentTurnID       uuid.UUID
	requestID          uuid.UUID
	liveSink           EventSink

	// userMessages is the root turn's queue. The root's waitMessages and ask
	// wait on it. A child never reads it.
	userMessages *activeUserMessageQueue
}

// handle is the tool Handler launchAgent registers.
func (d *launchAgentDeps) handle(
	ctx context.Context,
	input launchAgentInput,
) (launchAgentOutput, error) {
	return d.runtime.launchAgent(ctx, d, input)
}

// launchAgentTool builds the launchAgent tool over one turn's (or one
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

// launchAgent validates the requested child, enforces the depth and
// concurrency limits, records the run and its task, and starts the child in
// the background. It returns as soon as the child runs. The child's answer
// reaches its parent as a message when the child ends.
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

	executor, err := deps.executor.Fork(ctx)
	if err != nil {
		return launchAgentOutput{}, ctxerrors.Wrap(err, "fork child directory")
	}

	childDeps := *deps
	childDeps.executor = executor
	childDeps.directoryScope = nil
	deps = &childDeps

	return r.startChildLaunch(ctx, deps, input, prepared)
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

	skills, ledger, err := childTaskSkills(ctx, deps.snapshot, input.Task)
	if err != nil {
		return preparedChildLaunch{}, err
	}

	return preparedChildLaunch{
		definition:       definition,
		depth:            depth,
		systemPrompt:     systemPrompt,
		allowedToolsJSON: allowedToolsJSON,
		registry:         registry,
		skills:           skills,
		skillLedger:      ledger,
	}, nil
}

// childTaskSkills resolves the :name references in a child's task. The child's
// conversation starts with an empty ledger, whatever the parent has loaded.
func childTaskSkills(
	ctx context.Context,
	snapshot harness.Snapshot,
	task string,
) ([]skillMessage, *skillLedger, error) {
	ledger := newSkillLedger()

	skills, err := buildSkillMessages(ctx, snapshot, ledger, task)
	if err != nil {
		return nil, nil, err
	}

	ledger.recordMessages(skills)

	return skills, ledger, nil
}

// startChildLaunch records the run, its tagged task, and its start event,
// then runs the child on its own goroutine. A failure before the goroutine
// starts ends the run here and fails the launchAgent call.
func (r *Runtime) startChildLaunch(
	ctx context.Context,
	deps *launchAgentDeps,
	input launchAgentInput,
	prepared preparedChildLaunch,
) (launchAgentOutput, error) {
	sender := r.messageSender(ctx)
	sender.Text = input.Task

	task, err := renderAgentMessage(sender)
	if err != nil {
		return launchAgentOutput{}, ctxerrors.Wrap(err, "tag child task")
	}

	if native := r.nativeWorkflowRunner(); native != nil {
		return native.launchChild(ctx, deps, input, prepared, task, sender)
	}

	run, runCtx, sink, err := r.startChildAgentRun(ctx, deps, input, prepared)
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

	if err := r.openChildAgentRun(
		runCtx,
		deps,
		prepared,
		run,
		sink,
		task,
	); err != nil {
		r.endChildAgentRun(runCtx, prepared.registry, sink, run, nil, err)

		return launchAgentOutput{}, err
	}

	go r.superviseChildAgent(runCtx, deps, prepared, run, sink, task)

	return launchAgentOutput{
		AgentRunID:       run.ID,
		Name:             run.Name,
		Definition:       run.Definition,
		ParentAgentRunID: sender.Run,
	}, nil
}

// messageSender names the conversation running in ctx: the child run whose
// tool call this is, or the session's root conversation.
func (r *Runtime) messageSender(ctx context.Context) agentMessage {
	parent, ok := parentAgentRunFromContext(ctx)
	if !ok {
		return agentMessage{
			From: messageSenderAgent,
			Name: r.rootAgent,
			Run:  rootAgentRunReference,
		}
	}

	return agentMessage{
		From: messageSenderAgent,
		Name: parent.name,
		Run:  parent.id.String(),
	}
}

// openChildAgentRun makes the task the child's first durable message, so its
// transcript is complete from the request onward rather than from the first
// answer, and records that the child started.
func (r *Runtime) openChildAgentRun(
	runCtx context.Context,
	deps *launchAgentDeps,
	prepared preparedChildLaunch,
	run *AgentRun,
	sink *agentRunSink,
	task string,
) error {
	if err := sink.seedTask(runCtx, task, prepared.skills); err != nil {
		return err
	}

	return sink.emit(
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
	)
}

// superviseChildAgent runs one child to its end and reports the outcome. A
// panic still ends the run, so it never holds a concurrency slot forever.
func (r *Runtime) superviseChildAgent(
	runCtx context.Context,
	deps *launchAgentDeps,
	prepared preparedChildLaunch,
	run *AgentRun,
	sink *agentRunSink,
	task string,
) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}

		runtimecontext.Logger(runCtx).Error(
			"child agent panicked",
			"panic", recovered,
			"stack", string(debug.Stack()),
		)

		if run.Snapshot().State != AgentRunStateRunning {
			return
		}

		r.endChildAgentRun(
			runCtx,
			prepared.registry,
			sink,
			run,
			nil,
			ctxerrors.Wrapf(
				commerr.ErrInvalidState,
				"child agent panicked: %v",
				recovered,
			),
		)
	}()

	response, runErr := r.runChildAgent(runCtx, deps, prepared, task, run, sink)

	r.endChildAgentRun(
		runCtx,
		prepared.registry,
		sink,
		run,
		response,
		runErr,
	)
}

// endChildAgentRun closes the child's inbox, records how it ended, stops its
// running descendants, and hands its outcome to its parent.
func (r *Runtime) endChildAgentRun(
	runCtx context.Context,
	registry *AgentRunRegistry,
	sink *agentRunSink,
	run *AgentRun,
	response *elelem.Response,
	runErr error,
) {
	ctx := context.WithoutCancel(runCtx)

	unread := run.inbox.close()
	recordUndeliveredMessages(
		ctx, sink, unread, run.inbox.unreadAcceptanceIDs(),
	)

	result := r.finishAgentRun(ctx, registry, sink, run, response, runErr)

	if cancelled := registry.cancelDescendants(run.ID); cancelled > 0 {
		runtimecontext.Logger(ctx).Info(
			"child agent descendants cancelled",
			"reason", "parent_ended",
			"agent_runs", cancelled,
		)
	}

	r.reportToParentRun(ctx, registry, run, result)
}

// recordUndeliveredMessages notes each message the child ended before
// reading, so its sender's message is accounted for in the run's events.
func recordUndeliveredMessages(
	ctx context.Context,
	sink *agentRunSink,
	unread []string,
	acceptanceIDs []*uuid.UUID,
) {
	if len(unread) == 0 {
		return
	}

	runtimecontext.Logger(ctx).Warn(
		"child agent ended with unread messages",
		"reason", "agent_ended",
		"messages", len(unread),
	)

	for index, text := range unread {
		payload := agentInboxMessagePayload{Message: text}
		if index < len(acceptanceIDs) {
			payload.AcceptanceID = acceptanceIDs[index]
		}

		if err := sink.emit(
			ctx,
			EventTypeAgentRunMessageUndelivered,
			payload,
		); err != nil {
			runtimecontext.Logger(ctx).Warn(
				"record undelivered agent message",
				"err", err,
			)
		}
	}
}

// reportToParentRun puts a nested child's outcome in its parent run's inbox.
// A child of the root conversation reports through its completion event,
// which Finish publishes.
func (r *Runtime) reportToParentRun(
	ctx context.Context,
	registry *AgentRunRegistry,
	run *AgentRun,
	result AgentRunResult,
) {
	if run.ParentAgentRunID == nil {
		return
	}

	logger := runtimecontext.Logger(ctx)

	text, err := renderAgentMessage(agentMessage{
		From: messageSenderAgent,
		Name: run.Name,
		Run:  run.ID.String(),
		Kind: agentResultKind(result.State),
		Text: agentResultText(result),
	})
	if err != nil {
		logger.Error("render child agent result", "err", err)

		return
	}

	if err := registry.Deliver(ctx, *run.ParentAgentRunID, text); err != nil {
		logger.Warn(
			"child agent result not delivered",
			"reason", "parent_unavailable",
			"parent_agent_run_id", run.ParentAgentRunID.String(),
			"err", err,
		)
	}
}

// agentResultKind is the kind attribute of the message carrying an outcome.
func agentResultKind(state AgentRunState) string {
	switch state {
	case AgentRunStateCompleted:
		return messageKindResult
	case AgentRunStateCancelled:
		return messageKindCancelled
	default:
		return messageKindFailed
	}
}

// agentResultText is the body of the message carrying an outcome: the final
// answer, or what stopped the child.
func agentResultText(result AgentRunResult) string {
	switch result.State {
	case AgentRunStateCompleted:
		return result.Response
	case AgentRunStateCancelled:
		return agentCancelledText
	default:
		return agentFailedLead + result.FailureDetail
	}
}

// startChildAgentRun registers and records the run. Its sink and inbox exist
// before the run is visible to anyone, so a message can reach it from the
// moment it is listed.
func (r *Runtime) startChildAgentRun(
	ctx context.Context,
	deps *launchAgentDeps,
	input launchAgentInput,
	prepared preparedChildLaunch,
) (*AgentRun, context.Context, *agentRunSink, error) {
	var sink *agentRunSink

	run, runCtx, err := prepared.registry.Start(ctx, StartAgentRunInput{
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
		sink = newAgentRunSink(
			r.store,
			deps.sessionID,
			run,
			deps.liveSink,
			deps.modelReference,
		)

		inbox, err := newAgentInbox(sink, r.maxQueuedUserMessages)
		if err != nil {
			return err
		}

		run.inbox = inbox

		return r.createChildAgentRun(ctx, deps, input, prepared, run)
	})
	if err != nil {
		return nil, nil, nil, err
	}

	return run, runCtx, sink, nil
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
			ID:                 run.ID,
			ParentTurnID:       run.ParentTurnID,
			WorkerGenerationID: deps.workerGenerationID,
			ParentAgentRunID:   run.ParentAgentRunID,
			ParentToolCallID:   run.ParentToolCallID,
			RequestID:          run.RequestID,
			Name:               run.Name,
			Definition:         models.AgentRunDefinition(run.Definition),
			Depth:              int64(run.Depth),
			Workspace:          deps.executor.Workspace(),
			ModelReference:     deps.modelReference,
			ModelID:            deps.model.Model.ID,
			Task:               input.Task,
			Instructions:       prepared.definition.instructions,
			AllowedToolsJSON:   prepared.allowedToolsJSON,
			SystemPrompt:       prepared.systemPrompt,
			StartedAt:          run.StartedAt,
		},
	)
	if err != nil {
		return ctxerrors.Wrap(err, "create durable child agent")
	}

	return nil
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
	childCtx = contextWithParentAgentRun(childCtx, run)
	runtimecontext.Logger(childCtx).Info(
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

// childSystemPrompt assembles the child's system prompt from the same base
// system prompt, resolved rules, and skill catalogue the parent turn sees,
// with the child's own instructions (from a stored agent file or an ad-hoc
// definition) in place of the parent's root agent block.
//
// The base prompt leads, exactly as it does for the root. A child that ran
// without it still received the workspace rules and the tool catalogue, so it
// looked functional while missing the instructions that describe how Peen
// expects its tools to be used.
//
// PromptBlocks is asked for no root agent, so the parent's agent definition
// never lands in a child prompt. Per-turn PromptModeReplace and
// PromptModeAppend overrides stay out too: those belong to the user turn that
// requested them, not to every child the turn happens to launch.
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

	sections = append(sections, r.baseSystemPrompt)

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

// childPrompt is the child's first request: its system prompt, the task, and
// the skill messages that follow the task.
func childPrompt(
	systemPrompt string,
	task string,
	skills []skillMessage,
) elelem.Prompt {
	prompt := elelem.NewPrompt().WithSystem(systemPrompt).UserText(task)
	for _, skill := range skills {
		prompt = prompt.UserText(skill.text)
	}

	return prompt
}

// runChildAgent builds and runs one independent child conversation. Its event
// sink persists every observable callback before publishing the live tail.
//
//nolint:funlen // Keep child request lifecycle together.
func (r *Runtime) runChildAgent(
	runCtx context.Context,
	deps *launchAgentDeps,
	launch preparedChildLaunch,
	task string,
	run *AgentRun,
	sink *agentRunSink,
) (*elelem.Response, error) {
	depth := launch.depth

	prepared, err := r.prepareChildConversation(runCtx, deps, launch, task, run)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "prepare child conversation")
	}

	scope, maximumRounds := prepared.scope, prepared.maximumRounds

	requestSettingsJSON, err := newModelAuditSettings(
		deps.model.Model,
		true,
		maximumRounds,
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

	request := elelem.NewRequest(deps.model.Client).
		WithModel(deps.model.Model).
		WithPrompt(prepared.prompt).
		WithTools(scope.context().tools).
		WithToolProvider(scope.provideTools).
		WithAutoToolCalls().
		WithUserMessageQueue(run.inbox.queue).
		WithMaxRounds(prepared.remainingRounds).
		WithMaxConcurrentTools(r.maxConcurrentTools).
		WithToolTimeout(r.toolTimeout).
		WithMaxToolResultTokens(r.maxToolResultTokens).
		WithMaxContextTokens(r.maxContextTokens).
		OnText(sink.onText).
		OnReasoning(sink.onReasoning).
		OnRoundStart(scope.beforeRound).
		OnRoundStart(childRoundStart(run, audit)).
		OnRoundEnd(scope.afterRound).
		OnRoundEnd(audit.onRoundEnd).
		OnAssistantMessage(childAssistantCallback(sink, audit)).
		OnToolCallStart(sink.onToolCallStart).
		OnToolResult(func(
			ctx context.Context,
			call elelem.ToolCallEvent,
		) error {
			directory := scope.resultDirectory(call.CallID, "")

			return sink.persistToolResult(
				ctx, call, directory, scope.context().prompt,
			)
		}).
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
		})

	if prepared.compactor != nil {
		request = request.PreMaxTokensReached(prepared.compactor.handle)
	}

	response, err := scope.run(
		depthCtx, request, maximumRounds, 0,
	)

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

func childAssistantCallback(
	sink *agentRunSink,
	audit *modelAuditRecorder,
) func(context.Context, elelem.Message) error {
	return func(ctx context.Context, message elelem.Message) error {
		if err := sink.onAssistantMessage(ctx, message); err != nil {
			return ctxerrors.Wrap(err, "persist child assistant message")
		}

		return audit.onAssistantMessage(ctx, message)
	}
}

// childRoundStart records the inbox messages a round just took into the
// child's transcript, then audits the round.
func childRoundStart(
	run *AgentRun,
	audit *modelAuditRecorder,
) func(context.Context, *elelem.RoundEvent) error {
	return func(ctx context.Context, event *elelem.RoundEvent) error {
		if err := run.inbox.checkpointDelivered(
			ctx,
			event.Messages,
		); err != nil {
			return ctxerrors.Wrap(err, "checkpoint child inbox")
		}

		return audit.onRoundStart(ctx, event)
	}
}

func (r *Runtime) agentToolSet(
	deps *launchAgentDeps,
	skills *skillLedger,
	onPostRun elelem.MessageInjector,
	allowedTools []string,
	depth int,
) (*elelem.ToolSet, error) {
	toolSet := hostToolSet(
		deps.executor,
		onPostRun,
		deps.snapshot,
		skills,
		r.metrics,
	)
	if deps.directoryScope != nil {
		toolSet.Add(instrumentTool(hostTool(
			toolNameChangeDirectory, changeDirectoryDescription,
			changeDirectorySchema, deps.directoryScope.change, onPostRun,
		), r.metrics))
	}

	if depth < r.agentLimits.MaxDepth {
		toolSet.Add(
			instrumentTool(launchAgentTool(deps, onPostRun), r.metrics),
		)
	} else {
		allowedTools = withoutTool(allowedTools, toolNameLaunchAgent)
	}

	toolSet, allowedTools = r.extendToolSet(toolSet, allowedTools, depth)

	messaging := messageTools(deps, onPostRun)
	for _, tool := range messaging {
		toolSet.Add(instrumentTool(tool, r.metrics))
	}

	restricted, err := restrictToolSet(toolSet, allowedTools)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "restrict agent tools")
	}

	// allowed-tools limits what an agent can do in the workspace. The
	// session tools read retained history and communicate with other agents.
	for _, tool := range messaging {
		if _, found := restricted.Get(tool.Name); found {
			continue
		}

		restricted.Add(instrumentTool(tool, r.metrics))
	}

	return restricted, nil
}

func withoutTool(tools []string, excluded string) []string {
	if tools == nil {
		return nil
	}

	filtered := make([]string, 0, len(tools))
	for _, name := range tools {
		if name != excluded {
			filtered = append(filtered, name)
		}
	}

	return filtered
}

func isProcessTool(name string) bool {
	return name == toolNameRunCommand || name == toolNameListJobs ||
		name == toolNameReadJobOutput || name == toolNameWaitJob ||
		name == toolNameSignalJob
}

// finishAgentRun records the run's terminal state and returns the outcome its
// parent hears about. A run whose outcome cannot be recorded still ends, as
// failed, so it never holds a concurrency slot.
func (r *Runtime) finishAgentRun(
	ctx context.Context,
	registry *AgentRunRegistry,
	sink *agentRunSink,
	run *AgentRun,
	response *elelem.Response,
	runErr error,
) AgentRunResult {
	if runErr == nil && response == nil {
		runErr = ctxerrors.New("child provider returned no response")
	}

	state, classification, eventType := agentRunOutcome(runErr)

	result := AgentRunResult{State: state, FailureDetail: errorText(runErr)}
	if runErr == nil {
		result.Response = response.Text
	}

	if err := r.finalizeAgentRun(
		ctx,
		run,
		response,
		state,
		classification,
		runErr,
	); err != nil {
		runtimecontext.Logger(ctx).Error(
			"persist child agent outcome",
			"agent_run_id", run.ID.String(),
			"err", err,
		)

		result = AgentRunResult{
			State:         AgentRunStateFailed,
			FailureDetail: errorText(err),
		}
		registry.Finish(ctx, run, result)

		return result
	}

	registry.Finish(ctx, run, result)

	if err := sink.emit(ctx, eventType, agentRunTerminalPayload{
		AgentRunID:            run.ID,
		State:                 state,
		FailureClassification: classification,
		FailureDetail:         result.FailureDetail,
	}); err != nil {
		runtimecontext.Logger(ctx).Warn(
			"persist child agent terminal event",
			"agent_run_id", run.ID.String(),
			"err", err,
		)
	}

	logChildAgentOutcome(ctx, run, result, runErr)

	return result
}

func logChildAgentOutcome(
	ctx context.Context,
	run *AgentRun,
	result AgentRunResult,
	runErr error,
) {
	logger := runtimecontext.Logger(ctx)

	switch result.State {
	case AgentRunStateCompleted:
		logger.Info(
			"child agent completed",
			"agent_run_id", run.ID.String(),
			"agent_name", run.Name,
			"response_bytes", len(result.Response),
			"response_sha256", hash(result.Response),
		)
	case AgentRunStateCancelled:
		logger.Info(
			"child agent cancelled",
			"agent_run_id", run.ID.String(),
			"agent_name", run.Name,
		)
	default:
		logger.Warn(
			"child agent failed",
			"agent_run_id", run.ID.String(),
			"agent_name", run.Name,
			"state", result.State,
			"err", runErr,
		)
	}
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
	store          session.Storage
	sessionID      uuid.UUID
	run            *AgentRun
	liveSink       EventSink
	modelReference string
	phaseBuffer    *childPhaseBuffer
	mu             sync.Mutex
}

func newAgentRunSink(
	store session.Storage,
	sessionID uuid.UUID,
	run *AgentRun,
	liveSink EventSink,
	modelReference string,
) *agentRunSink {
	return &agentRunSink{
		store:          store,
		sessionID:      sessionID,
		run:            run,
		liveSink:       liveSink,
		modelReference: modelReference,
	}
}

// seedTask records the child's task as the first durable user message, so the
// child transcript begins with the request the way a session transcript begins
// with the user's. The skill messages that follow the task come right after
// it, in the order the child sees them.
func (s *agentRunSink) seedTask(
	ctx context.Context,
	task string,
	skills []skillMessage,
) error {
	inputs := make([]session.AgentRunMessageInput, 0, len(skills)+1)
	inputs = append(inputs, session.AgentRunMessageInput{
		Role:    models.MessageRoleUser,
		Content: task,
	})

	for _, skill := range skills {
		inputs = append(inputs, session.AgentRunMessageInput{
			Role:    models.MessageRoleUser,
			Content: skill.text,
		})
	}

	return s.persistMessages(ctx, inputs...)
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

	if s.phaseBuffer != nil {
		s.bufferPhaseEvent(eventType, string(encoded))

		return nil
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
	return s.persistToolResult(ctx, call, "", "")
}

func (s *agentRunSink) persistToolResult(
	ctx context.Context,
	call elelem.ToolCallEvent,
	directory, systemPrompt string,
) error {
	content, isError := "", false
	if call.Result != nil {
		content = call.Result.Content
		isError = call.Result.IsError
	}

	if err := s.persistMessages(ctx, session.AgentRunMessageInput{
		Role:         models.MessageRoleTool,
		Content:      content,
		ToolCallID:   call.CallID,
		IsError:      isError,
		Directory:    directory,
		SystemPrompt: systemPrompt,
	}); err != nil {
		return err
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
	toolCalls, err := json.Marshal(message.ToolCalls)
	if err != nil {
		return ctxerrors.Wrap(err, "marshal child assistant tool calls")
	}

	if err := s.persistMessages(ctx, session.AgentRunMessageInput{
		Role:          models.MessageRoleAssistant,
		Content:       message.Text(),
		ModelID:       s.modelReference,
		Thinking:      message.Reasoning,
		ToolCallsJSON: string(toolCalls),
	}); err != nil {
		return err
	}

	return s.emit(
		ctx,
		EventTypeAgentRunAssistantMessage,
		newAgentRunAssistantMessagePayload(message),
	)
}

func (s *agentRunSink) onMessageInjection(
	ctx context.Context,
	injection elelem.MessageInjection,
) error {
	role, err := injectedMessageRole(injection.Type)
	if err != nil {
		return ctxerrors.Wrap(err, "resolve child injection role")
	}

	if err := s.persistMessages(ctx, session.AgentRunMessageInput{
		Role:    role,
		Content: injection.Content,
	}); err != nil {
		return err
	}

	return s.emit(
		ctx,
		EventTypeAgentRunMessageInjected,
		agentRunMessageInjectedPayload{
			Type:    injection.Type,
			Content: injection.Content,
			Phase:   injection.Phase,
			Tool:    injection.Tool,
			CallID:  injection.CallID,
			Round:   injection.Round,
		},
	)
}

// persistMessages writes prompt-visible child records before the caller lets
// the child loop continue.
//
// The event log stays an event log. These rows are the child's conversation,
// which is what a compaction covers and what a restart replays.
func (s *agentRunSink) persistMessages(
	ctx context.Context,
	inputs ...session.AgentRunMessageInput,
) error {
	if len(inputs) == 0 {
		return nil
	}

	if s.phaseBuffer != nil {
		s.mu.Lock()
		s.phaseBuffer.Transcript.Messages = append(
			s.phaseBuffer.Transcript.Messages, inputs...)
		s.mu.Unlock()

		return nil
	}

	if _, err := s.store.AppendAgentRunMessages(
		ctx,
		s.sessionID,
		s.run.ID,
		inputs,
	); err != nil {
		return ctxerrors.Wrap(err, "append child transcript messages")
	}

	return nil
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

// agentRunAssistantToolCallPayload is one tool call on a child's assistant
// message. The vendored elelem.ToolCall has no JSON tags, so this is what
// reaches the stored row and the live socket frame instead of PascalCase
// keys.
type agentRunAssistantToolCallPayload struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// agentRunAssistantMessagePayload is a Peen-owned camelCase encoding of a
// child's assistant message, replacing a raw elelem.Message so the stored
// row and the live socket frame never leak the vendored struct. ToolCallID,
// ToolResultIsError, Injection and CacheHint are not carried: elelem only
// ever sets them on a RoleTool message or an injection-origin message,
// never on the message this callback receives.
type agentRunAssistantMessagePayload struct {
	Role    string         `json:"role"`
	Content elelem.Content `json:"content"`

	ToolCalls []agentRunAssistantToolCallPayload `json:"toolCalls"`

	Reasoning         string          `json:"reasoning,omitempty"`
	ProviderReasoning json.RawMessage `json:"providerReasoning,omitempty"`
	Origin            string          `json:"origin,omitempty"`
}

// newAgentRunAssistantMessagePayload builds the Peen-owned payload from the
// vendored message. Content and ToolCalls are always encoded as an array,
// never null, and ProviderReasoning is dropped when elelem left it empty or
// set to the JSON literal null.
func newAgentRunAssistantMessagePayload(
	message elelem.Message,
) agentRunAssistantMessagePayload {
	content := message.Content
	if content == nil {
		content = elelem.Content{}
	}

	toolCalls := make(
		[]agentRunAssistantToolCallPayload,
		len(message.ToolCalls),
	)
	for i, call := range message.ToolCalls {
		toolCalls[i] = agentRunAssistantToolCallPayload{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: call.Arguments,
		}
	}

	payload := agentRunAssistantMessagePayload{
		Role:      message.Role,
		Content:   content,
		ToolCalls: toolCalls,
		Reasoning: message.Reasoning,
		Origin:    message.Origin,
	}

	if !isEmptyOrNullRawJSON(message.ProviderReasoning) {
		payload.ProviderReasoning = message.ProviderReasoning
	}

	return payload
}

// jsonNullLiteral is the JSON encoding of null.
const jsonNullLiteral = "null"

// isEmptyOrNullRawJSON reports whether raw is absent or carries only the
// JSON literal null, which omitempty alone cannot tell from a meaningful
// one-token value.
func isEmptyOrNullRawJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)

	return len(trimmed) == 0 || string(trimmed) == jsonNullLiteral
}

// agentRunMessageInjectedPayload is a Peen-owned camelCase encoding of a
// child's tool-injected message, replacing a raw elelem.MessageInjection.
type agentRunMessageInjectedPayload struct {
	Type    string `json:"type"`
	Content string `json:"content"`
	Phase   string `json:"phase"`
	Tool    string `json:"tool"`
	CallID  string `json:"callId"`
	Round   int    `json:"round"`
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

func (r *Runtime) extendToolSet(
	toolSet *elelem.ToolSet,
	allowedTools []string,
	depth int,
) (*elelem.ToolSet, []string) {
	for _, tool := range r.extraTools {
		if tool.Name == toolNameLaunchAgent && depth >= r.agentLimits.MaxDepth {
			continue
		}

		toolSet.Add(instrumentTool(tool, r.metrics))
	}

	filtered := elelem.NewToolSet()

	for _, tool := range toolSet.Definitions() {
		if slices.Contains(r.disableTools, tool.Name) ||
			(r.disableProcessTools && isProcessTool(tool.Name)) {
			allowedTools = withoutTool(allowedTools, tool.Name)

			continue
		}

		filtered.Add(tool)
	}

	return filtered, allowedTools
}
