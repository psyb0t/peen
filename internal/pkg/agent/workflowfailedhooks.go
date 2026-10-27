package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const (
	// failedHookFailureFormat is the hook failure a hook step that failed
	// inside Peen settles with, given the hook event. The error itself goes
	// to the log.
	failedHookFailureFormat = "Peen failed while running the %s hooks"

	// failedHookEvidence records why the failed hook step was settled
	// without an inspection.
	//
	//nolint:lll // One sentence stored as the resolution evidence.
	failedHookEvidence = "the hook step failed inside Peen, so it is settled as a failed hook instead of waiting for an inspection"
)

func settlesAfterFailure(activityName string) bool {
	switch activityName {
	case toolPhaseActivityName, openingHookActivityName,
		compactionHookActivityName, terminalHooksActivityName:
		return true
	default:
		return false
	}
}

// settleFailedHook accepts a hook step as a failed hook. Each step's own
// failure rule then applies: an opening hook fails the turn, a pre-compaction
// hook refuses the summary, a post-compaction hook is logged, and terminal
// hooks leave the turn's result as it was.
func settleFailedHook(
	ctx context.Context,
	store session.ExecutionStorage,
	activity *models.ExecutionActivity,
) error {
	output, stepContext, err := failedHookOutput(activity)
	if err != nil {
		return err
	}

	encoded, err := json.Marshal(output)
	if err != nil {
		return ctxerrors.Wrap(err, "encode settled hook step")
	}

	if _, err := store.ResolveExecutionActivity(ctx, activity.SessionID,
		activity.ID, session.ResolveActivityInput{
			Attempt:    activity.Attempt,
			Resolution: session.ActivityResolutionAccept,
			Evidence:   failedHookEvidence,
			Output:     encoded,
			Context:    stepContext,
		}); err != nil {
		return ctxerrors.Wrap(err, "accept failed hook step")
	}

	return nil
}

func failedHookOutput(
	activity *models.ExecutionActivity,
) (any, json.RawMessage, error) {
	input := []byte(activity.InputJSON)

	switch activity.Name {
	case openingHookActivityName:
		var opening workflowOpeningInput
		if err := json.Unmarshal(input, &opening); err != nil {
			return nil, nil, ctxerrors.Wrap(err, "decode failed opening hook")
		}

		return workflowOpeningResult{
			Context: opening.Execution.Context,
			Failure: fmt.Sprintf(failedHookFailureFormat, opening.Event),
		}, opening.Execution.Context, nil
	case compactionHookActivityName:
		var compaction workflowCompactionHookInput
		if err := json.Unmarshal(input, &compaction); err != nil {
			return nil, nil, ctxerrors.Wrap(err,
				"decode failed compaction hook")
		}

		return workflowCompactionHookResult{
			Failure: fmt.Sprintf(failedHookFailureFormat, compaction.Event),
		}, compaction.Round.Execution.Context, nil
	case terminalHooksActivityName:
		var terminal workflowFinishInput
		if err := json.Unmarshal(input, &terminal); err != nil {
			return nil, nil, ctxerrors.Wrap(err, "decode failed terminal hooks")
		}

		return terminal, terminal.Result.Result.Context, nil
	default:
		return nil, nil, ctxerrors.Wrapf(commerr.ErrInvalidArgument,
			"%s is not a hook step", activity.Name)
	}
}
