package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/hooks"
	"github.com/psyb0t/peen/internal/pkg/tools"
)

const hookContextLead = "Configured hook context:\n"

type userMessageHookPayload struct {
	Message   string `json:"message"`
	Workspace string `json:"workspace"`
	Model     string `json:"model,omitempty"`
}

type turnHookPayload struct {
	Workspace string `json:"workspace"`
	Model     string `json:"model"`
}

type turnStopHookPayload struct {
	Workspace    string `json:"workspace"`
	Model        string `json:"model"`
	FinishReason string `json:"finishReason"`
}

// toolHookRuntime adapts a turn's immutable hook list to Elelem's tool
// lifecycle. It keeps action injections per call because different tool calls
// can run concurrently in the same tool round.
type toolHookRuntime struct {
	runner        hooks.Runner
	requestID     uuid.UUID
	sessionID     uuid.UUID
	turnID        uuid.UUID
	executor      *tools.JobExecutor
	sessionEvents elelem.MessageInjector

	mutex    sync.Mutex
	messages map[string][]string
	denials  map[string]string
}

func newToolHookRuntime(
	snapshot harness.Snapshot,
	workspace string,
	requestID uuid.UUID,
	sessionID uuid.UUID,
	turnID uuid.UUID,
	executor *tools.JobExecutor,
	sessionEvents elelem.MessageInjector,
	enableWorkspaceHooks bool,
	commandTimeoutDuration time.Duration,
	maxCommandOutput int,
	publisher hooks.EventPublisher,
) (*toolHookRuntime, error) {
	runner, err := hooks.New(hooks.Options{
		Snapshot:             snapshot,
		Workspace:            workspace,
		EnableWorkspaceHooks: enableWorkspaceHooks,
		CommandTimeout:       commandTimeoutDuration,
		MaxCommandOutput:     maxCommandOutput,
		Publisher:            publisher,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create tool hook runner")
	}

	return &toolHookRuntime{
		runner:        runner,
		requestID:     requestID,
		sessionID:     sessionID,
		turnID:        turnID,
		executor:      executor,
		sessionEvents: sessionEvents,
		messages:      map[string][]string{},
		denials:       map[string]string{},
	}, nil
}

func (r *Runtime) newToolHookRuntime(
	prepared *preparedTurn,
) (*toolHookRuntime, error) {
	runner, err := newToolHookRuntime(
		prepared.snapshot,
		prepared.workspace,
		prepared.turn.requestID,
		prepared.opened.Session.ID,
		prepared.lease.TurnID,
		prepared.executor,
		prepared.injectSessionEvents,
		r.enableWorkspaceHooks,
		r.hookCommandTimeout,
		r.maxHookCommandOutput,
		r.eventBus,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create turn hook runtime")
	}

	return runner, nil
}

func (r *Runtime) appendPreUserHookContext(
	ctx context.Context,
	snapshot harness.Snapshot,
	workspace string,
	input TurnRequest,
	systemPrompt string,
) (string, error) {
	runner, err := hooks.New(hooks.Options{
		Snapshot:             snapshot,
		Workspace:            workspace,
		EnableWorkspaceHooks: r.enableWorkspaceHooks,
		CommandTimeout:       r.hookCommandTimeout,
		MaxCommandOutput:     r.maxHookCommandOutput,
		Publisher:            r.eventBus,
	})
	if err != nil {
		return "", ctxerrors.Wrap(err, "create pre-user-message hook runner")
	}

	sessionID := uuid.Nil
	if input.SessionID != nil {
		sessionID = *input.SessionID
	}

	payload, err := json.Marshal(userMessageHookInput(input, workspace))
	if err != nil {
		return "", ctxerrors.Wrap(err, "encode pre-user-message hook input")
	}

	outcome, err := runner.Run(ctx, hooks.Invocation{
		Event:     harness.HookEventPreUserMessage,
		SessionID: sessionID,
		RequestID: input.RequestID,
		Workspace: workspace,
		Input:     payload,
	})
	if err != nil {
		return "", ctxerrors.Wrap(err, "run pre-user-message hooks")
	}

	return appendHookContext(systemPrompt, outcome.Injections), nil
}

func (p *preparedTurn) runLifecycleHook(
	ctx context.Context,
	runtime *Runtime,
	event harness.HookEvent,
	payload any,
) error {
	if p.toolHooks == nil {
		return nil
	}

	outcome, err := p.toolHooks.runLifecycle(ctx, event, payload)
	if err != nil {
		return ctxerrors.Wrap(err, "run lifecycle hooks")
	}

	if len(outcome.Injections) == 0 || !lifecycleInjectsBeforeModel(event) {
		return nil
	}

	p.prompt = p.prompt.AppendSystem(
		hookContextLead + strings.Join(outcome.Injections, "\n\n"),
	)

	promptHash, err := runtime.savePromptSnapshot(
		ctx,
		p.prompt.SystemMessage(),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "save lifecycle hook prompt snapshot")
	}

	p.promptHash = promptHash

	return nil
}

func (h *toolHookRuntime) runLifecycle(
	ctx context.Context,
	event harness.HookEvent,
	payload any,
) (hooks.Outcome, error) {
	input, err := json.Marshal(payload)
	if err != nil {
		return hooks.Outcome{}, ctxerrors.Wrap(
			err,
			"encode lifecycle hook input",
		)
	}

	outcome, err := h.runner.Run(ctx, hooks.Invocation{
		Event:     event,
		SessionID: h.sessionID,
		RequestID: h.requestID,
		TurnID:    h.turnID,
		Workspace: h.executor.Workspace(),
		Input:     input,
	})
	if err != nil {
		return hooks.Outcome{}, ctxerrors.Wrap(
			err,
			"run lifecycle hook actions",
		)
	}

	return outcome, nil
}

func userMessageHookInput(
	input TurnRequest,
	workspace string,
) userMessageHookPayload {
	return userMessageHookPayload{
		Message:   input.Message,
		Workspace: workspace,
		Model:     input.Model,
	}
}

func turnHookInput(prepared *preparedTurn) turnHookPayload {
	return turnHookPayload{
		Workspace: prepared.workspace,
		Model:     prepared.modelReference,
	}
}

func turnStopHookInput(
	prepared *preparedTurn,
	response *elelem.Response,
) turnStopHookPayload {
	return turnStopHookPayload{
		Workspace:    prepared.workspace,
		Model:        prepared.modelReference,
		FinishReason: string(response.FinishReason),
	}
}

func appendHookContext(systemPrompt string, messages []string) string {
	if len(messages) == 0 {
		return systemPrompt
	}

	return strings.Join(
		[]string{
			systemPrompt,
			hookContextLead + strings.Join(messages, "\n\n"),
		},
		systemSectionGap,
	)
}

func lifecycleInjectsBeforeModel(event harness.HookEvent) bool {
	return event == harness.HookEventSessionStart ||
		event == harness.HookEventPostUserMessage ||
		event == harness.HookEventTurnStart
}

func (h *toolHookRuntime) pre(
	ctx context.Context,
	event *elelem.ToolEvent,
) error {
	return h.run(ctx, event, preHookEvents(event.Tool.Name))
}

func (h *toolHookRuntime) success(
	ctx context.Context,
	event *elelem.ToolEvent,
) error {
	return h.run(ctx, event, successHookEvents(event.Tool.Name))
}

func (h *toolHookRuntime) failure(
	ctx context.Context,
	event *elelem.ToolEvent,
) error {
	return h.run(ctx, event, failureHookEvents(event.Tool.Name))
}

func (h *toolHookRuntime) post(
	ctx context.Context,
	event *elelem.ToolEvent,
) error {
	return h.run(ctx, event, []harness.HookEvent{harness.HookEventPostToolUse})
}

func (h *toolHookRuntime) run(
	ctx context.Context,
	event *elelem.ToolEvent,
	events []harness.HookEvent,
) error {
	invocation, err := h.invocation(event)
	if err != nil {
		h.deny(event, hookFailureMessage(err))

		return nil
	}

	for _, hookEvent := range events {
		invocation.Event = hookEvent

		outcome, runErr := h.runner.Run(ctx, invocation)
		if runErr != nil {
			h.deny(event, hookFailureMessage(runErr))

			return nil
		}

		h.add(event.CallID, outcome.Injections)
	}

	return nil
}

func (h *toolHookRuntime) invocation(
	event *elelem.ToolEvent,
) (hooks.Invocation, error) {
	result, err := json.Marshal(event.Result)
	if err != nil {
		return hooks.Invocation{}, ctxerrors.Wrap(
			err,
			"encode hook tool result",
		)
	}

	errText := ""
	if event.Err != nil {
		errText = toolErrorMessage(event.Err)
	}

	return hooks.Invocation{
		SessionID: h.sessionID,
		RequestID: h.requestID,
		TurnID:    h.turnID,
		Tool:      event.Tool.Name,
		CallID:    event.CallID,
		Workspace: h.executor.Workspace(),
		Paths:     h.paths(event.Tool.Name, event.RawArguments),
		Input:     append(json.RawMessage(nil), event.RawArguments...),
		Result:    result,
		Error:     errText,
	}, nil
}

func (h *toolHookRuntime) paths(
	toolName string,
	arguments json.RawMessage,
) []string {
	rawPaths := rawToolPaths(toolName, arguments)
	resolved := make([]string, 0, len(rawPaths))
	seen := map[string]struct{}{}

	for _, rawPath := range rawPaths {
		path, err := h.executor.ResolvePath(rawPath)
		if err != nil {
			continue
		}

		if _, exists := seen[path]; exists {
			continue
		}

		seen[path] = struct{}{}
		resolved = append(resolved, path)
	}

	return resolved
}

//nolint:cyclop // Each path-bearing tool needs its explicit argument decoder.
func rawToolPaths(toolName string, arguments json.RawMessage) []string {
	switch toolName {
	case toolNameReadFile,
		toolNameWriteFile,
		toolNameEditFile,
		toolNameRemovePath,
		toolNameListFiles,
		toolNameSearchText,
		toolNameMakeDirectory:
		input := struct {
			Path string `json:"path"`
		}{}
		if err := json.Unmarshal(arguments, &input); err != nil {
			return nil
		}

		return nonEmptyPath(input.Path)
	case toolNameMovePath:
		input := tools.MovePathInput{}
		if err := json.Unmarshal(arguments, &input); err != nil {
			return nil
		}

		return nonEmptyPaths(input.Source, input.Destination)
	case toolNameApplyPatch:
		input := tools.ApplyPatchInput{}
		if err := json.Unmarshal(arguments, &input); err != nil {
			return nil
		}

		paths, err := tools.ApplyPatchPaths(input.Patch)
		if err != nil {
			return nil
		}

		return paths
	case toolNameRunCommand:
		input := tools.RunCommandInput{}
		if err := json.Unmarshal(arguments, &input); err != nil {
			return nil
		}

		return nonEmptyPath(input.Directory)
	default:
		return nil
	}
}

func nonEmptyPath(path string) []string {
	return nonEmptyPaths(path)
}

func nonEmptyPaths(paths ...string) []string {
	nonEmpty := make([]string, 0, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) != "" {
			nonEmpty = append(nonEmpty, path)
		}
	}

	return nonEmpty
}

func preHookEvents(toolName string) []harness.HookEvent {
	return append(
		[]harness.HookEvent{harness.HookEventPreToolUse},
		fileHookEvent(toolName, hookEventPre)...,
	)
}

func successHookEvents(toolName string) []harness.HookEvent {
	return fileHookEvent(toolName, hookEventSuccess)
}

func failureHookEvents(toolName string) []harness.HookEvent {
	return append(
		[]harness.HookEvent{harness.HookEventToolUseFailure},
		fileHookEvent(toolName, hookEventFailure)...,
	)
}

type hookEventPhase string

const (
	hookEventPre     hookEventPhase = "pre"
	hookEventSuccess hookEventPhase = "success"
	hookEventFailure hookEventPhase = "failure"
)

func fileHookEvent(
	toolName string,
	phase hookEventPhase,
) []harness.HookEvent {
	event, found := fileHookEventByTool(toolName, phase)
	if !found {
		return nil
	}

	return []harness.HookEvent{event}
}

//nolint:cyclop // Explicit tool-to-event mapping avoids inferred event names.
func fileHookEventByTool(
	toolName string,
	phase hookEventPhase,
) (harness.HookEvent, bool) {
	switch toolName {
	case toolNameReadFile:
		return readFileHookEvent(phase)
	case toolNameListFiles:
		return listFilesHookEvent(phase)
	case toolNameSearchText:
		return searchTextHookEvent(phase)
	case toolNameWriteFile:
		return writeFileHookEvent(phase)
	case toolNameEditFile:
		return editFileHookEvent(phase)
	case toolNameApplyPatch:
		return applyPatchHookEvent(phase)
	case toolNameMovePath:
		return movePathHookEvent(phase)
	case toolNameRemovePath:
		return removePathHookEvent(phase)
	case toolNameMakeDirectory:
		return makeDirectoryHookEvent(phase)
	default:
		return "", false
	}
}

func readFileHookEvent(phase hookEventPhase) (harness.HookEvent, bool) {
	switch phase {
	case hookEventPre:
		return harness.HookEventPreReadFile, true
	case hookEventSuccess:
		return harness.HookEventPostReadFile, true
	case hookEventFailure:
		return harness.HookEventReadFileFailure, true
	default:
		return "", false
	}
}

func listFilesHookEvent(phase hookEventPhase) (harness.HookEvent, bool) {
	switch phase {
	case hookEventPre:
		return harness.HookEventPreListFiles, true
	case hookEventSuccess:
		return harness.HookEventPostListFiles, true
	case hookEventFailure:
		return harness.HookEventListFilesFailure, true
	default:
		return "", false
	}
}

func searchTextHookEvent(phase hookEventPhase) (harness.HookEvent, bool) {
	switch phase {
	case hookEventPre:
		return harness.HookEventPreSearchText, true
	case hookEventSuccess:
		return harness.HookEventPostSearchText, true
	case hookEventFailure:
		return harness.HookEventSearchTextFailure, true
	default:
		return "", false
	}
}

func writeFileHookEvent(phase hookEventPhase) (harness.HookEvent, bool) {
	switch phase {
	case hookEventPre:
		return harness.HookEventPreWriteFile, true
	case hookEventSuccess:
		return harness.HookEventPostWriteFile, true
	case hookEventFailure:
		return harness.HookEventWriteFileFailure, true
	default:
		return "", false
	}
}

func editFileHookEvent(phase hookEventPhase) (harness.HookEvent, bool) {
	switch phase {
	case hookEventPre:
		return harness.HookEventPreEditFile, true
	case hookEventSuccess:
		return harness.HookEventPostEditFile, true
	case hookEventFailure:
		return harness.HookEventEditFileFailure, true
	default:
		return "", false
	}
}

func applyPatchHookEvent(phase hookEventPhase) (harness.HookEvent, bool) {
	switch phase {
	case hookEventPre:
		return harness.HookEventPreApplyPatch, true
	case hookEventSuccess:
		return harness.HookEventPostApplyPatch, true
	case hookEventFailure:
		return harness.HookEventApplyPatchFailed, true
	default:
		return "", false
	}
}

func movePathHookEvent(phase hookEventPhase) (harness.HookEvent, bool) {
	switch phase {
	case hookEventPre:
		return harness.HookEventPreMovePath, true
	case hookEventSuccess:
		return harness.HookEventPostMovePath, true
	case hookEventFailure:
		return harness.HookEventMovePathFailure, true
	default:
		return "", false
	}
}

func removePathHookEvent(phase hookEventPhase) (harness.HookEvent, bool) {
	switch phase {
	case hookEventPre:
		return harness.HookEventPreRemovePath, true
	case hookEventSuccess:
		return harness.HookEventPostRemovePath, true
	case hookEventFailure:
		return harness.HookEventRemovePathFailed, true
	default:
		return "", false
	}
}

func makeDirectoryHookEvent(phase hookEventPhase) (harness.HookEvent, bool) {
	switch phase {
	case hookEventPre:
		return harness.HookEventPreMakeDirectory, true
	case hookEventSuccess:
		return harness.HookEventPostMakeDirectory, true
	case hookEventFailure:
		return harness.HookEventMakeDirectoryFailure, true
	default:
		return "", false
	}
}

func (h *toolHookRuntime) messageInjection(
	ctx context.Context,
	event *elelem.ToolEvent,
) (*elelem.MessageInjection, error) {
	configuredMessages := h.take(event.CallID)

	var sessionMessage *elelem.MessageInjection

	if h.sessionEvents != nil {
		injected, err := h.sessionEvents(ctx, event)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "inject session events")
		}

		sessionMessage = injected
	}

	if len(configuredMessages) == 0 {
		return sessionMessage, nil
	}

	content := hookContextLead + strings.Join(configuredMessages, "\n\n")
	if sessionMessage != nil {
		content += "\n\n" + sessionMessage.Content
	}

	return &elelem.MessageInjection{
		Type:    elelem.RoleUser,
		Content: content,
	}, nil
}

func (h *toolHookRuntime) add(callID string, messages []string) {
	if len(messages) == 0 {
		return
	}

	h.mutex.Lock()
	defer h.mutex.Unlock()

	h.messages[callID] = append(h.messages[callID], messages...)
}

func (h *toolHookRuntime) take(callID string) []string {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	messages := append([]string(nil), h.messages[callID]...)
	delete(h.messages, callID)

	return messages
}

func (h *toolHookRuntime) discard(callID string) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	delete(h.messages, callID)
}

func (h *toolHookRuntime) deny(event *elelem.ToolEvent, reason string) {
	h.discard(event.CallID)

	if event.Result != nil {
		result := elelem.NewToolErrorResult(reason)
		event.Result = &result

		return
	}

	h.mutex.Lock()
	defer h.mutex.Unlock()

	h.denials[event.CallID] = reason
}

func (h *toolHookRuntime) handler(
	handler elelem.ToolHandler,
) elelem.ToolHandler {
	return func(
		ctx context.Context,
		input elelem.ToolInput,
	) (elelem.ToolResult, error) {
		if reason, denied := h.takeDenial(input.CallID); denied {
			return elelem.NewToolErrorResult(reason), nil
		}

		return handler(ctx, input)
	}
}

func (h *toolHookRuntime) takeDenial(callID string) (string, bool) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	reason, found := h.denials[callID]
	delete(h.denials, callID)

	return reason, found
}

func hookFailureMessage(err error) string {
	message := toolErrorMessage(err)

	return strings.TrimSuffix(message, ": "+hooks.ErrDenied.Error())
}

func bindToolHooks(
	toolSet *elelem.ToolSet,
	runtime *toolHookRuntime,
) {
	if toolSet == nil || runtime == nil {
		return
	}

	for _, tool := range toolSet.Definitions() {
		tool.Handler = runtime.handler(tool.Handler)
		tool.PreRun = runtime.pre
		tool.OnSuccess = runtime.success
		tool.OnError = runtime.failure
		tool.PostRun = runtime.post
		tool.PostRunMessageInjector = runtime.messageInjection
		toolSet.Add(tool)
	}
}
