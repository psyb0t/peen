package agent

import (
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

func decodeChildSubmissionCall(
	activity *models.ExecutionActivity,
) (workflowToolInput, error) {
	if activity.Name != toolPhaseActivityName {
		var input workflowToolInput
		if err := json.Unmarshal([]byte(activity.InputJSON),
			&input); err != nil {
			return input, ctxerrors.Wrap(err,
				"decode retained child launch call")
		}

		return input, nil
	}

	var input workflowToolPhaseInput
	if err := json.Unmarshal([]byte(activity.InputJSON), &input); err != nil {
		return input.Tool, ctxerrors.Wrap(err,
			"decode phased child launch call")
	}

	if input.Phase != workflowToolPhaseHandler {
		return input.Tool, ctxerrors.Wrap(commerr.ErrConflict,
			"child launch does not belong to a handler phase")
	}

	if err := input.validate(); err != nil {
		return input.Tool, ctxerrors.Wrap(err,
			"validate phased child launch call")
	}

	return input.Tool, nil
}
