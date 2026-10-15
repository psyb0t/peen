package agent

import (
	"context"

	"github.com/psyb0t/ctxerrors"
)

func (d *workflowDispatch) afterModelRound(
	ctx context.Context,
	input workflowRoundInput,
	result workflowRoundResult,
) error {
	callbacks, err := d.callbacks(ctx, input.Execution)
	if err != nil {
		return ctxerrors.Wrap(err, "dispatch committed model round")
	}

	if callbacks.AfterModelRound == nil {
		return nil
	}

	return callbacks.AfterModelRound(ctx, input, result)
}

func (e *workflowExecution) deliverModelRound(
	ctx context.Context,
	input workflowRoundInput,
	result workflowActivityResult[workflowRoundResult],
) (workflowActivityResult[workflowRoundResult], error) {
	if e.options.AfterModelRound == nil {
		return result, nil
	}

	return deliverCommittedWorkflowResult(ctx, e, input.Execution, result,
		func(ctx context.Context, output workflowRoundResult) error {
			return e.options.AfterModelRound(ctx, input, output)
		})
}
