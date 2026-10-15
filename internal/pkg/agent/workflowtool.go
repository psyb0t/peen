package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/elelem"
)

const workflowEmptyArguments = `{}`

type workflowToolOptions struct {
	Timeout         time.Duration
	MaxResultTokens int
	MaxResultBytes  int
	Counter         elelem.TokenCounter
	Finish          func(string)
}

// Each activity runs the complete hook lifecycle for one recorded call.
func runWorkflowTool(
	ctx context.Context,
	set *elelem.ToolSet,
	input workflowToolInput,
	options workflowToolOptions,
) (workflowToolResult, error) {
	if options.Finish != nil {
		defer options.Finish(input.Call.ID)
	}

	if err := ctx.Err(); err != nil {
		return workflowToolResult{}, ctxerrors.Wrap(err,
			"workflow tool cancelled")
	}

	tool, found := set.Get(input.Call.Name)
	if !found {
		return boundedWorkflowToolResult(ctx, workflowToolResult{
			Result: elelem.NewToolErrorResult(
				fmt.Sprintf("unknown tool %q", input.Call.Name)),
		}, options)
	}

	timeout := tool.Timeout
	if timeout == 0 {
		timeout = options.Timeout
	}

	if timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	event, arguments, err := workflowToolEvent(tool, input)
	if err != nil {
		return boundedWorkflowToolResult(ctx, workflowToolResult{
			Result: elelem.NewToolErrorResult(toolErrorMessage(err)),
		}, options)
	}

	outcome, err := runWorkflowToolLifecycle(ctx, tool, event, arguments)
	if err != nil {
		return outcome, err
	}

	return boundedWorkflowToolResult(ctx, outcome, options)
}

func workflowToolEvent(
	tool elelem.Tool,
	input workflowToolInput,
) (*elelem.ToolEvent, json.RawMessage, error) {
	arguments := bytes.Clone(input.Call.Arguments)
	if len(bytes.TrimSpace(arguments)) == 0 {
		arguments = json.RawMessage(workflowEmptyArguments)
	}

	if !json.Valid(arguments) {
		return nil, nil, ctxerrors.Wrap(elelem.ErrInvalidRequest,
			"tool arguments are not valid JSON")
	}

	var conversation workflowConversation
	if err := json.Unmarshal(input.Round.Execution.Context,
		&conversation); err != nil {
		return nil, nil, ctxerrors.Wrap(err, "decode tool conversation")
	}

	tool.ArgumentsSchema = bytes.Clone(tool.ArgumentsSchema)

	return &elelem.ToolEvent{
		Tool: tool, CallID: input.Call.ID, Round: input.Round.Round,
		RawArguments: bytes.Clone(arguments), Messages: conversation.Messages,
	}, arguments, nil
}

func runWorkflowToolLifecycle(
	ctx context.Context,
	tool elelem.Tool,
	event *elelem.ToolEvent,
	arguments json.RawMessage,
) (workflowToolResult, error) {
	if err := workflowToolHook(ctx, event,
		elelem.ToolPhasePreRun, tool.PreRun); err != nil {
		if ctx.Err() != nil {
			return workflowToolResult{}, ctxerrors.Wrap(err,
				"tool pre-hook interrupted")
		}

		return failedWorkflowToolResult(err), nil
	}

	if err := ctx.Err(); err != nil {
		return workflowToolResult{}, ctxerrors.Wrap(err,
			"tool activity interrupted before handler")
	}

	if tool.Handler == nil {
		return workflowToolResult{Result: elelem.NewToolErrorResult(
			"tool has no handler")}, nil
	}

	if err := runWorkflowToolHandler(ctx, tool, event, arguments); err != nil {
		return workflowToolResult{}, err
	}

	outcome := workflowPrimaryToolHooks(ctx, tool, event)
	if outcome.Failure == "" {
		if err := workflowToolHook(ctx, event,
			elelem.ToolPhasePostRun, tool.PostRun); err != nil {
			outcome.Failure = toolErrorMessage(err)
		}
	}

	outcome = workflowToolInjection(ctx, event,
		elelem.ToolPhasePostRun, tool.PostRunMessageInjector, outcome)
	if event.Result == nil {
		replacement := elelem.NewToolErrorResult("tool hook removed result")
		event.Result = &replacement
	}

	outcome.Result = *event.Result
	if isWorkflowToolInterrupted(ctx, event.Err) {
		return outcome, ctxerrors.Wrap(errors.Join(event.Err, ctx.Err()),
			"tool activity interrupted")
	}

	return outcome, nil
}

func runWorkflowToolHandler(
	ctx context.Context,
	tool elelem.Tool,
	event *elelem.ToolEvent,
	arguments json.RawMessage,
) error {
	result, handlerErr := tool.Handler(ctx, elelem.ToolInput{
		Name: tool.Name, CallID: event.CallID,
		Arguments: bytes.Clone(arguments),
	})
	if isWorkflowToolInterrupted(ctx, handlerErr) {
		return ctxerrors.Wrap(errors.Join(handlerErr, ctx.Err()),
			"tool handler interrupted")
	}

	if handlerErr != nil {
		result = elelem.NewToolErrorResult(toolErrorMessage(handlerErr))
		event.Err = handlerErr
	}

	event.Result = &result

	return nil
}

func isWorkflowToolInterrupted(ctx context.Context, err error) bool {
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil
}

func failedWorkflowToolResult(err error) workflowToolResult {
	return workflowToolResult{
		Result:  elelem.NewToolErrorResult(toolErrorMessage(err)),
		Failure: toolErrorMessage(err),
	}
}

func workflowPrimaryToolHooks(
	ctx context.Context,
	tool elelem.Tool,
	event *elelem.ToolEvent,
) workflowToolResult {
	phase, hook, injector := elelem.ToolPhaseOnSuccess,
		tool.OnSuccess, tool.OnSuccessMessageInjector
	if event.Err != nil || event.Result.IsError {
		phase, hook, injector = elelem.ToolPhaseOnError,
			tool.OnError, tool.OnErrorMessageInjector
	}

	outcome := workflowToolResult{}
	if err := workflowToolHook(ctx, event, phase, hook); err != nil {
		outcome.Failure = toolErrorMessage(err)
	}

	return workflowToolInjection(ctx, event, phase, injector, outcome)
}

func workflowToolHook(
	ctx context.Context,
	event *elelem.ToolEvent,
	phase elelem.ToolPhase,
	hook elelem.ToolHook,
) error {
	if hook == nil {
		return nil
	}

	event.Phase = phase
	if err := hook(ctx, event); err != nil {
		return ctxerrors.Wrap(err, "workflow tool hook")
	}

	return nil
}

func workflowToolInjection(
	ctx context.Context,
	event *elelem.ToolEvent,
	phase elelem.ToolPhase,
	injector elelem.MessageInjector,
	outcome workflowToolResult,
) workflowToolResult {
	if outcome.Failure != "" || injector == nil {
		return outcome
	}

	event.Phase = phase

	injection, err := injector(ctx, event)
	if err != nil {
		outcome.Failure = toolErrorMessage(err)

		return outcome
	}

	if injection == nil {
		return outcome
	}

	if !validWorkflowInjectionRole(injection.Type) {
		ctxscope.GetLogger(ctx).Error("tool injection has an unusable role",
			"reason", elelem.LogReasonInjectionRoleInvalid,
			"tool", event.Tool.Name)

		return outcome
	}

	copied := *injection
	copied.Phase, copied.Tool = phase, event.Tool.Name
	copied.CallID, copied.Round = event.CallID, event.Round
	outcome.Injections = append(outcome.Injections, copied)

	return outcome
}

func validWorkflowInjectionRole(role elelem.Role) bool {
	return role == elelem.RoleUser || role == elelem.RoleAssistant ||
		role == elelem.RoleSystem
}
