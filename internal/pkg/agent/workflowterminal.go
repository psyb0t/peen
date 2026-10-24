package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/go-workflows/workflow"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const (
	terminalHooksActivityName = session.ExecutionTerminalHooksActivityName
)

func (e *workflowExecution) terminalHooks(
	ctx context.Context,
	input workflowFinishInput,
) (workflowActivityResult[workflowFinishInput], error) {
	if input.Round < 0 || input.Round >= input.Execution.MaxRounds {
		return workflowActivityResult[workflowFinishInput]{},
			ctxerrors.Wrap(commerr.ErrValidationFailed, "terminal hook round")
	}

	return runWorkflowCheckpoint(ctx, e, input.Execution,
		session.ScheduleActivityInput{
			StepID: fmt.Sprintf("terminalHooks:%d", input.Round),
			Kind:   models.ActivityKindTool, Name: terminalHooksActivityName,
			ReplayPolicy: models.ActivityReplayPolicyReconcile,
		}, input, func(ctx context.Context) (workflowFinishInput, error) {
			if e.options.TerminalHooks == nil {
				return input, nil
			}

			return e.options.TerminalHooks(ctx, input)
		}, func(output workflowFinishInput) json.RawMessage {
			return output.Result.Result.Context
		})
}

func (d *workflowDispatch) terminalHooks(
	ctx context.Context,
	input workflowFinishInput,
) (workflowFinishInput, error) {
	callbacks, err := d.callbacks(ctx, input.Execution)
	if err != nil {
		return input, ctxerrors.Wrap(err, "dispatch terminal hooks")
	}

	if callbacks.TerminalHooks == nil {
		return input, nil
	}

	return callbacks.TerminalHooks(ctx, input)
}

func terminalWorkflowFinish(
	ctx workflow.Context,
	input workflowExecutionInput,
	result workflowRoundResult,
	round int,
) (workflowFinishResult, error) {
	finish := workflowFinishInput{
		Execution: input, Round: round,
		Result: workflowFinishResult{
			Result: result, AllowContinuation: true,
			Finish: session.FinishExecutionInput{
				State: models.ExecutionStateCompleted,
			},
		},
	}

	prepared, err := awaitWorkflowActivity[workflowFinishInput](ctx,
		terminalHooksActivityName, finish)
	if err != nil {
		return workflowFinishResult{}, ctxerrors.Wrap(err,
			"execute terminal hook checkpoint")
	}

	if prepared.Result.Result.Continue {
		prepared.Result.Continue = true

		return prepared.Result, nil
	}

	return awaitWorkflowActivity[workflowFinishResult](ctx,
		session.ExecutionFinishActivityName, prepared)
}
