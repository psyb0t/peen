package agent

import (
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/go-workflows/workflow"
)

func compactingWorkflowModelRound(
	ctx workflow.Context,
	request workflowRoundInput,
) (workflowRoundResult, error) {
	prepared, err := awaitWorkflowActivity[json.RawMessage](ctx,
		prepareActivityName, request)
	if err != nil {
		return workflowRoundResult{Context: request.Execution.Context},
			ctxerrors.Wrap(err, "prepare compacting workflow round")
	}

	request.Execution.Context = prepared

	compacted, err := awaitWorkflowActivity[workflowCompactionResult](ctx,
		compactionActivityName, request)
	if err != nil {
		return workflowRoundResult{Context: prepared},
			ctxerrors.Wrap(err, "compact prepared workflow round")
	}

	request.Execution.Context = compacted.Context

	result, err := awaitWorkflowActivity[workflowRoundResult](ctx,
		modelActivityName, request)
	if err != nil {
		return workflowRoundResult{Context: compacted.Context},
			ctxerrors.Wrap(err, "run compacted model round")
	}

	return result, nil
}
