package agent

import (
	"context"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func deliverCommittedWorkflowResult[T any](
	ctx context.Context,
	e *workflowExecution,
	input workflowExecutionInput,
	result workflowActivityResult[T],
	deliver func(context.Context, T) error,
) (workflowActivityResult[T], error) {
	if deliver == nil ||
		(result.Decision != session.ActivityDecisionExecute &&
			result.Decision != session.ActivityDecisionReuse) {
		return result, nil
	}

	ctx, finish := e.activityContext(ctx, input)
	defer finish()

	_, err := invokeWorkflowPhase(ctx, func(ctx context.Context) (
		struct{}, error,
	) {
		return struct{}{}, deliver(ctx, result.Output)
	})
	if err == nil {
		return result, nil
	}

	err = e.pauseForCapacity(ctx, input, err)

	execution, readErr := e.options.Store.GetExecution(
		context.WithoutCancel(ctx), input.SessionID, input.ExecutionID)
	if readErr != nil {
		return result, ctxerrors.Wrap(errors.Join(err, readErr),
			"read interrupted checkpoint delivery")
	}

	if execution.State == models.ExecutionStatePaused ||
		rootAdmissionRefused(execution, err) {
		result.Decision = session.ActivityDecisionPaused

		return result, nil
	}

	return result, ctxerrors.Wrap(err, "deliver committed workflow result")
}
