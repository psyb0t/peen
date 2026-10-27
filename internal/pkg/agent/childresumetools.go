package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const (
	// interruptedToolResultFormat is the tool result a resumed child reads
	// for a call that a pause interrupted. %s is the pause reason.
	//
	//nolint:lll // The sentence is one line in the tool result.
	interruptedToolResultFormat = "This tool call was interrupted when your work was paused (%s). Its outcome is unknown: check the workspace before you repeat it."

	// interruptedToolEvidence records why the interrupted call was settled
	// without an inspection by the agent.
	//
	//nolint:lll // One sentence stored as the resolution evidence.
	interruptedToolEvidence = "a message resumed the paused child, so the interrupted tool call is reported to the model as an error instead of being repeated"

	// failedToolResult is the tool result the model reads for a call that
	// failed inside Peen. The error itself goes to the log.
	//
	//nolint:lll // The sentence is one line in the tool result.
	failedToolResult = "Peen failed while running this tool call, so its outcome is unknown. Check the workspace before you repeat it."

	// failedToolEvidence records why the failed call was settled without an
	// inspection by the agent.
	//
	//nolint:lll // One sentence stored as the resolution evidence.
	failedToolEvidence = "the tool call failed inside Peen, so it is reported to the model as an error instead of waiting for an inspection"
)

// settleInterruptedTools reports every tool call a pause interrupted in the
// executions a message resumed as an error result the model reads, so the
// resumed work continues instead of waiting on an inspection nobody makes.
// The call is not repeated, because its effect may already have happened.
func (n *nativeWorkflowRunner) settleInterruptedTools(
	ctx context.Context,
	sessionID uuid.UUID,
	executionIDs []uuid.UUID,
) error {
	store := n.engine.options.Store
	settled := 0

	for _, executionID := range executionIDs {
		execution, err := store.GetExecution(ctx, sessionID, executionID)
		if err != nil {
			return ctxerrors.Wrap(err, "read resumed execution")
		}

		count, err := settleExecutionInterruptedTools(ctx, store, execution)
		if err != nil {
			return ctxerrors.Wrap(err, "settle interrupted tool calls")
		}

		settled += count
	}

	if settled > 0 {
		ctxscope.GetLogger(ctx).Info("interrupted tool calls settled",
			"session_id", sessionID, "settled_count", settled,
			"reason", "message_resumed_child")
	}

	return nil
}

func settleExecutionInterruptedTools(
	ctx context.Context,
	store session.ExecutionStorage,
	execution *models.Execution,
) (int, error) {
	settled := 0
	options := session.ListEventsOptions{Limit: executionReadPageLimit}

	for {
		page, err := store.ListExecutionActivities(ctx, execution.SessionID,
			execution.ID, options)
		if err != nil {
			return settled, ctxerrors.Wrap(err, "list resumed activities")
		}

		for _, activity := range page.Items {
			if activity.State != models.ActivityStateUncertain ||
				activity.Name != toolPhaseActivityName {
				continue
			}

			if err := settleInterruptedTool(ctx, store, activity,
				fmt.Sprintf(interruptedToolResultFormat, execution.PauseReason),
				interruptedToolEvidence); err != nil {
				return settled, err
			}

			settled++
		}

		if !page.HasMore {
			return settled, nil
		}

		options.Offset += len(page.Items)
	}
}

// settleInterruptedTool accepts the interrupted phase with its saved state,
// an error result, and Finished set, so the workflow projects that result to
// the model and runs no further phase of the call.
func settleInterruptedTool(
	ctx context.Context,
	store session.ExecutionStorage,
	activity *models.ExecutionActivity,
	result, evidence string,
) error {
	var input workflowToolPhaseInput
	if err := json.Unmarshal([]byte(activity.InputJSON), &input); err != nil {
		return ctxerrors.Wrap(err, "decode interrupted tool phase")
	}

	// The saved projections belong to the phase before this one. The project
	// phase that follows records this result itself.
	state := input.State
	state.RootProjection, state.ChildProjection = nil, nil
	state.LastPhase = input.Phase
	state.Finished = true
	state.Outcome.Failure = ""
	state.Outcome.Result = elelem.NewToolErrorResult(result)

	output, err := json.Marshal(state)
	if err != nil {
		return ctxerrors.Wrap(err, "encode settled tool phase")
	}

	if _, err := store.ResolveExecutionActivity(ctx, activity.SessionID,
		activity.ID, session.ResolveActivityInput{
			Attempt:    activity.Attempt,
			Resolution: session.ActivityResolutionAccept,
			Evidence:   evidence,
			Output:     output,
			Context:    workflowToolPhaseTurnContext(input, state),
		}); err != nil {
		return ctxerrors.Wrap(err, "accept interrupted tool call as failed")
	}

	return nil
}
