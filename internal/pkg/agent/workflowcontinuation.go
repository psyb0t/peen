package agent

import (
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/go-workflows/workflow"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func finishFailedWorkflow(
	ctx workflow.Context,
	input workflowExecutionInput,
	result workflowRoundResult,
	cause error,
) (workflowRoundResult, error) {
	if len(result.Context) == 0 {
		result.Context = input.Context
	}

	saved, err := awaitWorkflowActivity[workflowFinishResult](ctx,
		session.ExecutionFinishActivityName, workflowFinishInput{
			Execution: input, Round: input.MaxRounds,
			Result: workflowFinishResult{
				Result: result,
				Finish: session.FinishExecutionInput{
					State:         models.ExecutionStateFailed,
					FailureDetail: cause.Error(),
				},
			},
		})
	if err != nil {
		return result, ctxerrors.Wrap(err, "finish failed workflow execution")
	}

	return saved.Result, cause
}
