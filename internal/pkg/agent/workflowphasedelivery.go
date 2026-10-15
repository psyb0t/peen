package agent

import (
	"context"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (e *workflowExecution) deliverToolPhase(
	ctx context.Context,
	input workflowToolPhaseInput,
	result workflowActivityResult[workflowToolPhaseState],
) (workflowActivityResult[workflowToolPhaseState], error) {
	if e.options.AfterToolPhase == nil ||
		(result.Decision != session.ActivityDecisionExecute &&
			result.Decision != session.ActivityDecisionReuse) {
		return result, nil
	}

	_, err := invokeWorkflowPhase(ctx, func(ctx context.Context) (
		struct{}, error,
	) {
		return struct{}{}, e.options.AfterToolPhase(ctx, input, result.Output)
	})
	if err != nil {
		err = e.pauseForCapacity(ctx, input.Tool.Round.Execution, err)

		execution, readErr := e.options.Store.GetExecution(
			context.WithoutCancel(ctx), input.Tool.Round.Execution.SessionID,
			input.Tool.Round.Execution.ExecutionID)
		if readErr != nil {
			return result, ctxerrors.Wrap(errors.Join(err, readErr),
				"read interrupted phase delivery state")
		}

		if execution.State == models.ExecutionStatePaused ||
			rootAdmissionRefused(execution, err) {
			result.Decision = session.ActivityDecisionPaused

			return result, nil
		}

		return result, ctxerrors.Wrap(err, "deliver committed tool phase")
	}

	return result, nil
}
