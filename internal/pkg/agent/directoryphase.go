package agent

import (
	"context"

	"github.com/psyb0t/ctxerrors"
)

func runDirectoryWorkflowToolPhase(
	ctx context.Context,
	scope *directoryScope,
	input workflowToolPhaseInput,
	options workflowToolOptions,
) (workflowToolPhaseState, error) {
	defer scope.finishTool(input.Tool.Call.ID)

	if err := scope.restoreWorkflowPendingDirectory(ctx,
		input.State.Pending); err != nil {
		return input.State, ctxerrors.Wrap(err, "restore staged tool directory")
	}

	registered, err := scope.provideTools(ctx)
	if err != nil {
		return input.State, ctxerrors.Wrap(err, "provide phase tools")
	}

	options.Finish = nil

	state, err := runWorkflowToolPhase(ctx, registered, input,
		workflowPhaseExecutionOptions{
			Tool: options, Bind: scope.bindWorkflowTool,
		})
	if err != nil {
		return state, ctxerrors.Wrap(err, "execute directory tool phase")
	}

	state.Pending, err = scope.workflowPendingDirectory()
	if err != nil {
		return state, ctxerrors.Wrap(err, "retain staged tool directory")
	}

	state.Outcome.Context, err = rootWorkflowToolContext(input.Tool, scope)
	if err != nil {
		return state, ctxerrors.Wrap(err, "retain tool phase directory context")
	}

	return state, nil
}
