package agent

import (
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/go-workflows/workflow"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/publicerror"
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

	// The stored detail reaches clients and the parent model, so it carries
	// the public text of the failure and no source locations.
	cause = restoreWorkflowSentinels(cause)

	saved, err := awaitWorkflowActivity[workflowFinishResult](ctx,
		session.ExecutionFinishActivityName, workflowFinishInput{
			Execution: input, Round: input.MaxRounds,
			Result: workflowFinishResult{
				Result: result,
				Finish: session.FinishExecutionInput{
					State:         models.ExecutionStateFailed,
					FailureDetail: publicerror.Message(cause),
				},
			},
		})
	if err != nil {
		return result, ctxerrors.Wrap(err, "finish failed workflow execution")
	}

	return saved.Result, workflowFailureCause(cause)
}
