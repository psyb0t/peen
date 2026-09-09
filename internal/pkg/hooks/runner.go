package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/harness"
)

const (
	pathSeparator     = "/"
	parentDirectory   = ".."
	jsonPointerEscape = "~"
	jsonPointerSlash  = "~1"
	jsonPointerTilde  = "~0"

	hookStateDirectoryName = "hook-state"
	hookStateDirectoryMode = 0o700

	hookRunOutcomeSucceeded = "succeeded"
	hookRunOutcomeFailed    = "failed"
)

// Runner executes one turn's immutable, resolved hook list.
type Runner struct {
	hooks                []harness.Hook
	workspace            string
	enableWorkspaceHooks bool
	commandTimeout       time.Duration
	maxCommandOutput     int
	publisher            EventPublisher
	runCommand           CommandRunner
	stateRoot            string
	contextTokenCounter  ContextTokenCounter
}

// New validates execution bounds and captures a snapshot's hook list.
func New(options Options) (Runner, error) {
	if strings.TrimSpace(options.Workspace) == "" {
		return Runner{}, ctxerrors.Wrap(
			ErrDenied,
			"hook workspace is required",
		)
	}

	if options.CommandTimeout <= 0 {
		options.CommandTimeout = defaultCommandTimeout
	}

	if options.MaxCommandOutput <= 0 {
		options.MaxCommandOutput = defaultCommandOutput
	}

	if options.RunCommand == nil {
		options.RunCommand = runCommand
	}

	if strings.TrimSpace(options.StateRoot) == "" {
		configRoot := filepath.Clean(options.Snapshot.ConfigRoot())
		if configRoot == "." || strings.TrimSpace(configRoot) == "" {
			return Runner{}, ctxerrors.Wrap(
				ErrDenied,
				"hook state root is required",
			)
		}

		options.StateRoot = filepath.Join(
			configRoot,
			hookStateDirectoryName,
		)
	}

	return Runner{
		hooks:                options.Snapshot.Hooks(),
		workspace:            filepath.Clean(options.Workspace),
		enableWorkspaceHooks: options.EnableWorkspaceHooks,
		commandTimeout:       options.CommandTimeout,
		maxCommandOutput:     options.MaxCommandOutput,
		publisher:            options.Publisher,
		runCommand:           options.RunCommand,
		stateRoot:            filepath.Clean(options.StateRoot),
		contextTokenCounter:  options.ContextTokenCounter,
	}, nil
}

// Run evaluates matching groups and actions serially. Config-root groups are
// always executable. Workspace groups require explicit runtime opt-in.
//
//nolint:funlen,nonamedreturns // One serial lifecycle boundary.
func (r Runner) Run(
	ctx context.Context,
	invocation Invocation,
) (outcome Outcome, runErr error) {
	invocation.Workspace = r.workspace
	ctx = ctxscope.Set(
		ctx,
		ctxscope.Attr("hook_event", string(invocation.Event)),
	)
	startedAt := time.Now()
	matchedHookCount := 0
	executedActionCount := 0

	ctxscope.GetLogger(ctx).Debug(
		"hook lifecycle event started",
		"tool_name", invocation.Tool,
		"tool_call_id", invocation.CallID,
	)
	defer func() {
		ctxscope.GetLogger(ctx).Debug(
			"hook lifecycle event finished",
			"outcome", hookRunOutcome(runErr),
			"matched_hook_count", matchedHookCount,
			"executed_action_count", executedActionCount,
			"injection_count", len(outcome.Injections),
			"tool_name", invocation.Tool,
			"tool_call_id", invocation.CallID,
			"duration_ms", time.Since(startedAt).Milliseconds(),
		)
	}()

	for _, hook := range r.hooks {
		if hook.Event != invocation.Event ||
			(!hook.ConfigLayer && !r.enableWorkspaceHooks) {
			continue
		}

		if !matches(hook.Match, invocation) {
			continue
		}

		matchedHookCount++
		hookCtx := ctxscope.Set(
			ctx,
			ctxscope.Attr("hook_name", resolvedHookName(hook)),
		)
		hookStartedAt := time.Now()
		executedBefore := executedActionCount
		injectionsBefore := len(outcome.Injections)
		ctxscope.GetLogger(hookCtx).Debug(
			"hook group started",
			"hook_source", hook.Source,
			"hook_action_count", len(hook.Actions),
			"tool_name", invocation.Tool,
			"tool_call_id", invocation.CallID,
		)

		err := r.runHookActions(
			hookCtx,
			hook,
			invocation,
			&outcome,
			&executedActionCount,
		)
		ctxscope.GetLogger(hookCtx).Debug(
			"hook group finished",
			"hook_source", hook.Source,
			"outcome", hookRunOutcome(err),
			"executed_action_count", executedActionCount-executedBefore,
			"injection_count", len(outcome.Injections)-injectionsBefore,
			"tool_name", invocation.Tool,
			"tool_call_id", invocation.CallID,
			"duration_ms", time.Since(hookStartedAt).Milliseconds(),
		)

		if err != nil {
			return outcome, err
		}
	}

	return outcome, nil
}

//nolint:funlen // Per-action logging and failure policy must stay adjacent.
func (r Runner) runHookActions(
	ctx context.Context,
	hook harness.Hook,
	invocation Invocation,
	outcome *Outcome,
	executedActionCount *int,
) error {
	for actionIndex, action := range hook.Actions {
		if !matches(action.When, invocation) {
			continue
		}

		*executedActionCount++
		actionName := resolvedActionName(action, actionIndex)
		actionCtx := ctxscope.Set(
			ctx,
			ctxscope.Attr("hook_name", resolvedHookName(hook)),
			ctxscope.Attr("hook_action_name", actionName),
		)
		startedAt := time.Now()

		ctxscope.GetLogger(actionCtx).Debug(
			"hook action started",
			"hook_action_type", action.Type,
			"hook_source", hook.Source,
			"tool_name", invocation.Tool,
			"tool_call_id", invocation.CallID,
		)

		result, actionErr := r.runAction(actionCtx, action, invocation)
		ctxscope.GetLogger(actionCtx).Debug(
			"hook action finished",
			"hook_action_type", action.Type,
			"hook_source", hook.Source,
			"tool_name", invocation.Tool,
			"tool_call_id", invocation.CallID,
			"outcome", hookRunOutcome(actionErr),
			"injection_count", len(result.Injections),
			"duration_ms", time.Since(startedAt).Milliseconds(),
		)

		if actionErr != nil {
			if errors.Is(actionErr, ErrDenied) {
				ctxscope.GetLogger(actionCtx).Warn(
					"hook action denied operation",
					"hook_action_type", action.Type,
					"hook_source", hook.Source,
					"tool_name", invocation.Tool,
					"tool_call_id", invocation.CallID,
					"err", actionErr,
				)

				return ctxerrors.Wrap(actionErr, "run hook action")
			}

			if r.continuesOnFailure(invocation.Event, action) {
				r.recordActionFailure(
					actionCtx,
					hook,
					action,
					invocation,
					actionErr,
				)

				continue
			}

			return ctxerrors.Wrap(actionErr, "run hook action")
		}

		outcome.Injections = append(outcome.Injections, result.Injections...)
	}

	return nil
}

func (r Runner) continuesOnFailure(
	event harness.HookEvent,
	action harness.HookAction,
) bool {
	if action.OnFailure == harness.HookFailureContinue {
		return true
	}

	if action.OnFailure == harness.HookFailureDeny {
		return false
	}

	return !isPreEvent(event)
}

func isPreEvent(event harness.HookEvent) bool {
	return strings.HasPrefix(string(event), "pre_") ||
		event == harness.HookEventSessionStart ||
		event == harness.HookEventTurnStart
}

type actionResult struct {
	Injections []string
}

func (r Runner) runAction(
	ctx context.Context,
	action harness.HookAction,
	invocation Invocation,
) (actionResult, error) {
	switch action.Type {
	case harness.HookActionInject:
		return actionResult{Injections: []string{action.Message}}, nil
	case harness.HookActionDeny:
		return actionResult{}, ctxerrors.Wrap(ErrDenied, action.Reason)
	case harness.HookActionEmitEvent:
		return actionResult{}, r.publishActionEvent(ctx, action, invocation)
	case harness.HookActionCommand:
		return r.runCommandAction(ctx, action, invocation)
	default:
		return actionResult{}, ctxerrors.Wrap(
			ErrDenied,
			"unsupported resolved hook action",
		)
	}
}

//nolint:funlen,nonamedreturns // One command lifecycle boundary.
func (r Runner) runCommandAction(
	ctx context.Context,
	action harness.HookAction,
	invocation Invocation,
) (result actionResult, runErr error) {
	commandInvocation, payload, err := r.commandInvocation(ctx, invocation)
	if err != nil {
		return actionResult{}, err
	}

	timeout := r.commandTimeout
	if action.TimeoutSeconds > 0 {
		timeout = time.Duration(action.TimeoutSeconds) * time.Second
	}

	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	startedAt := time.Now()

	ctxscope.GetLogger(ctx).Debug(
		"hook command started",
		"context_tokens", commandInvocation.ContextTokens,
		"has_state_directory", commandInvocation.StateDirectory != "",
	)
	defer func() {
		ctxscope.GetLogger(ctx).Debug(
			"hook command finished",
			"outcome", hookRunOutcome(runErr),
			"duration_ms", time.Since(startedAt).Milliseconds(),
		)
	}()

	output, err := r.runCommand(commandCtx, CommandInput{
		Command:     action.Command,
		Args:        append([]string(nil), action.Args...),
		Environment: cloneEnvironment(action.Environment),
		WorkingDir:  actionWorkingDirectory(r.workspace, action.WorkingDir),
		Stdin:       append([]byte(nil), payload...),
		MaxOutput:   r.maxCommandOutput,
	})
	if err != nil {
		return actionResult{}, ctxerrors.Wrap(err, "run hook command")
	}

	decision, found, err := decodeCommandDecision(output)
	if err != nil {
		return actionResult{}, ctxerrors.Wrap(err, "decode hook command result")
	}

	if !found {
		return actionResult{}, nil
	}

	if err := r.publishCommandEvents(
		ctx,
		decision.Events,
		commandInvocation,
	); err != nil {
		return actionResult{}, ctxerrors.Wrap(
			err,
			"publish hook command events",
		)
	}

	if decision.Decision == commandDecisionDeny {
		reason := strings.TrimSpace(decision.Reason)
		if reason == "" {
			reason = "hook command denied operation"
		}

		return actionResult{}, ctxerrors.Wrap(ErrDenied, reason)
	}

	return actionResult{Injections: nonEmptyString(decision.Message)}, nil
}

func (r Runner) commandInvocation(
	ctx context.Context,
	invocation Invocation,
) (Invocation, []byte, error) {
	if !invocation.HasContextTokenEstimate && r.contextTokenCounter != nil {
		contextTokens, err := r.contextTokenCounter(ctx, invocation)
		if err != nil {
			return Invocation{}, nil, ctxerrors.Wrap(
				err,
				"estimate hook context tokens",
			)
		}

		if contextTokens < 0 {
			return Invocation{}, nil, ctxerrors.Wrap(
				ErrInvalidContextTokens,
				"hook context token estimate is negative",
			)
		}

		invocation.ContextTokens = contextTokens
	}

	if invocation.SessionID != uuid.Nil {
		stateDirectory, err := r.sessionStateDirectory(invocation.SessionID)
		if err != nil {
			return Invocation{}, nil, ctxerrors.Wrap(
				err,
				"create hook session state directory",
			)
		}

		invocation.StateDirectory = stateDirectory
	}

	payload, err := invocationPayload(invocation)
	if err != nil {
		return Invocation{}, nil, ctxerrors.Wrap(err, "encode hook invocation")
	}

	return invocation, payload, nil
}

func (r Runner) sessionStateDirectory(sessionID uuid.UUID) (string, error) {
	if r.stateRoot == "." || strings.TrimSpace(r.stateRoot) == "" {
		return "", ctxerrors.Wrap(ErrDenied, "hook state root is required")
	}

	if err := os.MkdirAll(r.stateRoot, hookStateDirectoryMode); err != nil {
		return "", ctxerrors.Wrap(err, "create hook state root")
	}

	if err := os.Chmod(r.stateRoot, hookStateDirectoryMode); err != nil {
		return "", ctxerrors.Wrap(err, "protect hook state root")
	}

	directory := filepath.Join(r.stateRoot, sessionID.String())
	if err := os.MkdirAll(directory, hookStateDirectoryMode); err != nil {
		return "", ctxerrors.Wrap(err, "create hook session state")
	}

	if err := os.Chmod(directory, hookStateDirectoryMode); err != nil {
		return "", ctxerrors.Wrap(err, "protect hook session state")
	}

	return directory, nil
}

func nonEmptyString(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}

	return []string{value}
}

func decodeCommandDecision(output []byte) (commandDecision, bool, error) {
	if len(bytes.TrimSpace(output)) == 0 {
		return commandDecision{}, false, nil
	}

	if !json.Valid(output) {
		return commandDecision{}, false, nil
	}

	decision := commandDecision{}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&decision); err != nil {
		return commandDecision{}, false, ctxerrors.Wrap(
			err,
			"decode hook command decision",
		)
	}

	if decision.Decision != "" && decision.Decision != commandDecisionAllow &&
		decision.Decision != commandDecisionDeny {
		return commandDecision{}, false, ctxerrors.Wrap(
			ErrDenied,
			"hook command returned an invalid decision",
		)
	}

	return decision, true, nil
}

func (r Runner) publishActionEvent(
	ctx context.Context,
	action harness.HookAction,
	invocation Invocation,
) error {
	data, err := json.Marshal(action.Data)
	if err != nil {
		return ctxerrors.Wrap(err, "encode hook event data")
	}

	return r.publishEvent(ctx, emittedEvent{
		Type:     action.EventType,
		Summary:  action.Summary,
		Data:     data,
		Delivery: action.Delivery,
	}, invocation)
}

func (r Runner) publishCommandEvents(
	ctx context.Context,
	eventsToPublish []emittedEvent,
	invocation Invocation,
) error {
	for _, event := range eventsToPublish {
		if err := r.publishEvent(ctx, event, invocation); err != nil {
			return err
		}
	}

	return nil
}

//nolint:nonamedreturns // The deferred lifecycle log needs the final outcome.
func (r Runner) publishEvent(
	ctx context.Context,
	event emittedEvent,
	invocation Invocation,
) (publishErr error) {
	startedAt := time.Now()

	ctxscope.GetLogger(ctx).Debug(
		"hook session event started",
		"hook_emitted_event_type", event.Type,
		"hook_emitted_event_delivery", event.Delivery,
	)
	defer func() {
		ctxscope.GetLogger(ctx).Debug(
			"hook session event finished",
			"hook_emitted_event_type", event.Type,
			"hook_emitted_event_delivery", event.Delivery,
			"outcome", hookRunOutcome(publishErr),
			"duration_ms", time.Since(startedAt).Milliseconds(),
		)
	}()

	if r.publisher == nil || invocation.SessionID == uuid.Nil {
		return ErrEventUnavailable
	}

	delivery, err := events.ValidateDelivery(event.Delivery)
	if err != nil {
		return ctxerrors.Wrap(err, "validate hook event delivery")
	}

	if err := events.ValidateType(event.Type); err != nil {
		return ctxerrors.Wrap(err, "validate hook event type")
	}

	_, err = r.publisher.Publish(events.Notice{
		SessionID: invocation.SessionID,
		Type:      event.Type,
		Source:    hookFailureEventSource,
		Summary:   event.Summary,
		Data:      append(json.RawMessage(nil), event.Data...),
		Delivery:  delivery,
	})
	if err != nil {
		return ctxerrors.Wrap(err, "publish hook event")
	}

	return nil
}

func (r Runner) recordActionFailure(
	ctx context.Context,
	hook harness.Hook,
	action harness.HookAction,
	invocation Invocation,
	cause error,
) {
	ctxscope.GetLogger(ctx).Warn(
		"hook action failed and continued",
		"err", cause,
		"hook_action_type", action.Type,
		"hook_source", hook.Source,
	)

	if r.publisher == nil || invocation.SessionID == uuid.Nil {
		return
	}

	data, err := json.Marshal(map[string]string{
		"event":       string(invocation.Event),
		"hook_name":   resolvedHookName(hook),
		"action":      action.Name,
		"action_type": string(action.Type),
		"source":      hook.Source,
	})
	if err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"encode hook failure event data failed",
			"err", err,
		)

		return
	}

	_, err = r.publisher.Publish(events.Notice{
		SessionID: invocation.SessionID,
		Type:      hookFailureEventType,
		Source:    hookFailureEventSource,
		Summary:   "Hook action failed after the operation.",
		Data:      data,
		Delivery:  events.DeliveryQueue,
	})
	if err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"publish hook failure event failed",
			"err", err,
		)
	}
}

func resolvedHookName(hook harness.Hook) string {
	if strings.TrimSpace(hook.Name) != "" {
		return hook.Name
	}

	return string(hook.Event)
}

func resolvedActionName(action harness.HookAction, actionIndex int) string {
	if strings.TrimSpace(action.Name) != "" {
		return action.Name
	}

	return fmt.Sprintf("%s-%d", action.Type, actionIndex+1)
}

func hookRunOutcome(err error) string {
	if err == nil {
		return hookRunOutcomeSucceeded
	}

	return hookRunOutcomeFailed
}

func invocationPayload(invocation Invocation) ([]byte, error) {
	encoded, err := json.Marshal(invocation)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "marshal hook invocation")
	}

	return encoded, nil
}

func actionWorkingDirectory(workspace string, requested string) string {
	if strings.TrimSpace(requested) == "" {
		return workspace
	}

	if filepath.IsAbs(requested) {
		return filepath.Clean(requested)
	}

	return filepath.Join(workspace, requested)
}

func cloneEnvironment(environment map[string]string) map[string]string {
	cloned := make(map[string]string, len(environment))
	maps.Copy(cloned, environment)

	return cloned
}

func runCommand(ctx context.Context, input CommandInput) ([]byte, error) {
	arguments := append([]string(nil), input.Args...)
	//nolint:gosec // Commands come from resolved hook configuration.
	command := exec.CommandContext(ctx, input.Command, arguments...)
	command.Dir = input.WorkingDir
	command.Env = commandEnvironment(input.Environment)
	command.Stdin = bytes.NewReader(input.Stdin)
	stdout := newLimitedBuffer(input.MaxOutput)
	stderr := newLimitedBuffer(input.MaxOutput)
	command.Stdout = stdout
	command.Stderr = stderr

	err := command.Run()

	if stdout.exceeded || stderr.exceeded {
		return nil, ErrCommandOutputLimit
	}

	if err != nil {
		return nil, ctxerrors.Wrap(err, "run hook executable")
	}

	return stdout.Bytes(), nil
}

type limitedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func newLimitedBuffer(limit int) *limitedBuffer {
	return &limitedBuffer{limit: limit}
}

func (b *limitedBuffer) Write(content []byte) (int, error) {
	if b.limit <= 0 || b.buffer.Len()+len(content) <= b.limit {
		written, err := b.buffer.Write(content)
		if err != nil {
			return written, ctxerrors.Wrap(err, "write hook command output")
		}

		return written, nil
	}

	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if _, err := b.buffer.Write(content[:remaining]); err != nil {
			return 0, ctxerrors.Wrap(err, "write bounded hook command output")
		}
	}

	b.exceeded = true

	return len(content), ErrCommandOutputLimit
}

func (b *limitedBuffer) Bytes() []byte {
	return append([]byte(nil), b.buffer.Bytes()...)
}

func commandEnvironment(environment map[string]string) []string {
	values := make([]string, 1, 1+len(environment))
	values[0] = "PATH=" + os.Getenv("PATH")
	keys := make([]string, 0, len(environment))

	for key := range environment {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	for _, key := range keys {
		values = append(values, key+"="+environment[key])
	}

	return values
}

func matches(
	match harness.HookMatch,
	invocation Invocation,
) bool {
	if match.Tool != "" && match.Tool != invocation.Tool {
		return false
	}

	if !matchesPaths(match, invocation) {
		return false
	}

	return matchesInput(match.Input, invocation.Input)
}

func matchesPaths(match harness.HookMatch, invocation Invocation) bool {
	if match.Path == "" && len(match.Extensions) == 0 && match.Root == "" {
		return true
	}

	for _, affectedPath := range invocation.Paths {
		if matchesPath(match, invocation.Workspace, affectedPath) {
			return true
		}
	}

	return false
}

func matchesPath(
	match harness.HookMatch,
	workspace string,
	affectedPath string,
) bool {
	pathForMatch := filepath.Clean(affectedPath)

	base := filepath.Clean(workspace)
	if match.Root != "" {
		base = actionWorkingDirectory(workspace, match.Root)
	}

	relative, err := filepath.Rel(base, pathForMatch)
	outsideBase := relative == parentDirectory ||
		strings.HasPrefix(relative, parentDirectory+string(filepath.Separator))

	if err != nil || outsideBase {
		return false
	}

	if len(match.Extensions) > 0 && !matchesExtension(
		filepath.Ext(pathForMatch),
		match.Extensions,
	) {
		return false
	}

	if match.Path == "" {
		return true
	}

	return matchGlob(match.Path, filepath.ToSlash(relative))
}

func matchesExtension(extension string, allowed []string) bool {
	return slices.Contains(allowed, extension)
}

func matchGlob(pattern string, value string) bool {
	var expression strings.Builder
	expression.WriteString("^")

	for index := 0; index < len(pattern); {
		switch pattern[index] {
		case '*':
			if index+1 < len(pattern) && pattern[index+1] == '*' {
				isDirectoryPattern := index+2 < len(pattern) &&
					pattern[index+2] == pathSeparator[0]

				if isDirectoryPattern {
					expression.WriteString("(?:.*/)?")

					index += 3

					continue
				}

				expression.WriteString(".*")

				index += 2

				continue
			}

			expression.WriteString("[^/]*")
		case '?':
			expression.WriteString("[^/]")
		default:
			expression.WriteString(regexp.QuoteMeta(string(pattern[index])))
		}

		index++
	}

	expression.WriteString("$")
	matched, err := regexp.MatchString(expression.String(), value)

	return err == nil && matched
}

//nolint:cyclop // Keep each optional predicate explicit.
func matchesInput(
	conditions map[string]harness.HookInputMatch,
	input json.RawMessage,
) bool {
	if len(conditions) == 0 {
		return true
	}

	if len(bytes.TrimSpace(input)) == 0 {
		input = json.RawMessage("{}")
	}

	data := any(nil)
	if err := json.Unmarshal(input, &data); err != nil {
		return false
	}

	for pointer, condition := range conditions {
		value, exists := jsonPointerValue(data, pointer)
		if condition.Exists != nil && *condition.Exists != exists {
			return false
		}

		if condition.Equals != nil && !sameJSONValue(condition.Equals, value) {
			return false
		}

		if condition.Regex != "" &&
			!matchHookRegex(condition.Regex, value, exists) {
			return false
		}
	}

	return true
}

func jsonPointerValue(data any, pointer string) (any, bool) {
	if pointer == "" {
		return data, true
	}

	current := data
	segments := strings.SplitSeq(
		strings.TrimPrefix(pointer, pathSeparator),
		pathSeparator,
	)

	for segment := range segments {
		segment = strings.ReplaceAll(segment, jsonPointerSlash, pathSeparator)
		segment = strings.ReplaceAll(
			segment,
			jsonPointerTilde,
			jsonPointerEscape,
		)

		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}

		value, ok := object[segment]
		if !ok {
			return nil, false
		}

		current = value
	}

	return current, true
}

func sameJSONValue(left any, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)

	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func matchHookRegex(pattern string, value any, exists bool) bool {
	if !exists {
		return false
	}

	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}

	return compiled.MatchString(fmt.Sprint(value))
}
