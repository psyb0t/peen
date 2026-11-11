package agent

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
)

func projectWorkflowToolResult(
	ctx context.Context,
	set *elelem.ToolSet,
	input workflowToolPhaseInput,
	options workflowPhaseExecutionOptions,
) (workflowToolPhaseState, error) {
	state := input.State
	if input.Tool.Compact && !state.CompactFinished && !state.Finished {
		var err error

		state, err = finishAcceptedCompactTool(ctx, set, input, options)
		if err != nil {
			return state, ctxerrors.Wrap(err, "finish accepted tool lifecycle")
		}
	}

	outcome, err := boundedWorkflowToolResult(ctx, state.Outcome, options.Tool)
	state.Outcome, state.LastPhase = outcome, input.Phase

	return state, err
}

// An inspected handler result has not run the internal outcome and post
// processing. Finish those in projection without repeating its handler.
func finishAcceptedCompactTool(
	ctx context.Context,
	set *elelem.ToolSet,
	input workflowToolPhaseInput,
	options workflowPhaseExecutionOptions,
) (workflowToolPhaseState, error) {
	if set == nil {
		return input.State, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"accepted compact tool set")
	}

	tool, found := set.Get(input.Tool.Call.Name)
	if !found {
		return input.State, ctxerrors.Wrap(commerr.ErrNotFound,
			"accepted compact tool")
	}

	working := input
	working.Tool.Compact = false

	for _, phase := range []workflowToolPhase{
		workflowToolPhaseOutcome, workflowToolPhasePost,
	} {
		working.Phase = phase

		state, err := executeWorkflowToolPhase(ctx, tool, working, options)
		if err != nil {
			return state, ctxerrors.Wrap(err,
				"finish inspected tool processing")
		}

		working.State = state
		options.Bind = nil

		if state.Finished {
			break
		}
	}

	working.State.CompactFinished = true

	outcome, err := finishWorkflowRepeat(ctx, input.Tool,
		working.State.Outcome, options.Tool)
	if err != nil {
		return working.State, ctxerrors.Wrap(err,
			"retain inspected tool result")
	}

	working.State.Outcome = outcome

	return working.State, nil
}
