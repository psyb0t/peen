package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
)

// workflowToolEventState is what a tool event keeps between phases. Its
// messages are not kept: each phase rebuilds them from the turn context in
// its input, which no phase changes.
type workflowToolEventState struct {
	RawArguments json.RawMessage `json:"rawArguments"`
	HandlerError string          `json:"handlerError,omitempty"`
}

type workflowPhaseExecutionOptions struct {
	Tool workflowToolOptions
	Bind func(context.Context, *elelem.ToolEvent) error
}

func runWorkflowToolPhase(
	ctx context.Context,
	set *elelem.ToolSet,
	input workflowToolPhaseInput,
	options workflowPhaseExecutionOptions,
) (workflowToolPhaseState, error) {
	if options.Tool.Finish != nil {
		defer options.Tool.Finish(input.Tool.Call.ID)
	}

	if err := input.validate(); err != nil {
		return input.State, ctxerrors.Wrap(err, "validate tool phase execution")
	}

	if err := ctx.Err(); err != nil {
		return input.State, ctxerrors.Wrap(err, "tool phase cancelled")
	}

	if input.Phase == workflowToolPhaseProject {
		return projectWorkflowToolResult(ctx, set, input, options)
	}

	if set == nil {
		return input.State, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"tool phase tool set")
	}

	tool, found := set.Get(input.Tool.Call.Name)
	if !found {
		state := input.State
		state.Outcome.Result = elelem.NewToolErrorResult(
			fmt.Sprintf("unknown tool %q", input.Tool.Call.Name))
		state.Finished, state.LastPhase = true, input.Phase

		return state, nil
	}

	return runKnownWorkflowToolPhase(ctx, set, tool, input, options)
}

func runKnownWorkflowToolPhase(
	ctx context.Context,
	set *elelem.ToolSet,
	tool elelem.Tool,
	input workflowToolPhaseInput,
	options workflowPhaseExecutionOptions,
) (workflowToolPhaseState, error) {
	if input.Tool.Compact && input.Phase == workflowToolPhaseHandler {
		return runCompactToolHandler(ctx, set, input, options.Tool)
	}

	if input.Phase == workflowToolPhaseHandler {
		return runRepeatCheckedPhase(ctx, tool, input, options)
	}

	state, err := executeWorkflowToolPhase(ctx, tool, input, options)
	if err != nil || input.Phase != workflowToolPhasePost {
		return state, err
	}

	state.Outcome, err = finishWorkflowRepeat(ctx, input.Tool, state.Outcome,
		options.Tool)

	return state, err
}

func runCompactToolHandler(
	ctx context.Context,
	set *elelem.ToolSet,
	input workflowToolPhaseInput,
	options workflowToolOptions,
) (workflowToolPhaseState, error) {
	state := input.State
	outcome, err := runWorkflowTool(ctx, set, input.Tool, options)
	state.Outcome, state.LastPhase = outcome, input.Phase
	state.CompactFinished = err == nil

	return state, err
}

func runRepeatCheckedPhase(
	ctx context.Context,
	tool elelem.Tool,
	input workflowToolPhaseInput,
	options workflowPhaseExecutionOptions,
) (workflowToolPhaseState, error) {
	tool, repetition := repeatCheckedWorkflowTool(tool, input.Tool)
	state, err := executeWorkflowToolPhase(ctx, tool, input, options)
	state.Outcome.Repeat = repetition

	return state, err
}

func executeWorkflowToolPhase(
	ctx context.Context,
	tool elelem.Tool,
	input workflowToolPhaseInput,
	options workflowPhaseExecutionOptions,
) (workflowToolPhaseState, error) {
	state := input.State

	ctx, cancel := workflowToolPhaseContext(ctx, tool, options.Tool, &state)
	defer cancel()

	if err := ctx.Err(); err != nil {
		return state, ctxerrors.Wrap(err, "tool phase deadline expired")
	}

	event, arguments, err := workflowToolEvent(tool, input.Tool)
	if err != nil {
		state.Outcome.Result = elelem.NewToolErrorResult(toolErrorMessage(err))
		state.Finished, state.LastPhase = true, input.Phase

		return state, nil
	}

	if err := restoreWorkflowToolEvent(input.Phase, event, state); err != nil {
		return state, ctxerrors.Wrap(err, "restore tool phase event")
	}

	if input.Phase != workflowToolPhasePre && options.Bind != nil {
		if err := options.Bind(ctx, event); err != nil {
			return state, ctxerrors.Wrap(err, "restore phase tool binding")
		}
	}

	ctx = workflowPassiveWaitContext(ctx, input)

	err = executeWorkflowToolEffect(ctx, tool, event, arguments,
		input.Phase, &state)
	if isWorkflowToolInterrupted(ctx, err) {
		return state, ctxerrors.Wrap(errors.Join(err, ctx.Err()),
			"tool phase interrupted")
	}

	if err != nil {
		state.Outcome.Failure = toolErrorMessage(err)
		state.Finished = true

		if input.Phase == workflowToolPhasePre {
			state.Outcome = failedWorkflowToolResult(err)
		}
	}

	retainWorkflowToolEvent(event, input.Phase, &state)
	retainWorkflowPassiveWait(input, &state, time.Now().UTC())

	return state, nil
}

func workflowToolPhaseContext(
	ctx context.Context,
	tool elelem.Tool,
	options workflowToolOptions,
	state *workflowToolPhaseState,
) (context.Context, context.CancelFunc) {
	if state.LastPhase == "" && state.Deadline.IsZero() {
		timeout := tool.Timeout
		if timeout == 0 {
			timeout = options.Timeout
		}

		if timeout > 0 {
			state.Deadline = time.Now().UTC().Add(timeout)
		}
	}

	if state.Deadline.IsZero() {
		return context.WithCancel(ctx)
	}

	return context.WithDeadline(ctx, state.Deadline)
}

func restoreWorkflowToolEvent(
	phase workflowToolPhase,
	event *elelem.ToolEvent,
	state workflowToolPhaseState,
) error {
	if phase == workflowToolPhasePre {
		return nil
	}

	if state.Event == nil || !json.Valid(state.Event.RawArguments) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"saved tool phase event is missing or invalid")
	}

	event.RawArguments = bytes.Clone(state.Event.RawArguments)

	if phase != workflowToolPhaseHandler {
		result := state.Outcome.Result
		event.Result = &result
	}

	if state.Event.HandlerError != "" {
		event.Err = ctxerrors.New(state.Event.HandlerError)
	}

	return nil
}

func retainWorkflowToolEvent(
	event *elelem.ToolEvent,
	phase workflowToolPhase,
	state *workflowToolPhaseState,
) {
	state.LastPhase = phase
	state.Event = &workflowToolEventState{
		RawArguments: bytes.Clone(event.RawArguments),
	}

	if event.Err != nil {
		state.Event.HandlerError = toolErrorMessage(event.Err)
	}

	if event.Result != nil {
		state.Outcome.Result = *event.Result

		return
	}

	if phase != workflowToolPhasePre {
		state.Outcome.Result = elelem.NewToolErrorResult(
			"tool hook removed result")
	}
}
