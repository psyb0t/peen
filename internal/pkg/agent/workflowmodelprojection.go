package agent

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func validateCommittedModelRound(
	ctx context.Context,
	runtime *Runtime,
	input workflowRoundInput,
	result workflowRoundResult,
	activityID uuid.UUID,
) error {
	store, ok := runtime.store.(session.ExecutionStorage)
	if !ok {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"committed model storage")
	}

	activity, err := store.GetExecutionActivity(ctx, input.Execution.SessionID,
		activityID)
	if err != nil {
		return ctxerrors.Wrap(err, "read committed model activity")
	}

	encodedInput, err := json.Marshal(input)
	if err != nil {
		return ctxerrors.Wrap(err, "encode committed model input")
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		return ctxerrors.Wrap(err, "encode committed model result")
	}

	if !sameCommittedModelProjection(activity, input, string(encodedInput),
		string(encoded)) {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"model differs from committed activity")
	}

	return nil
}

func sameCommittedModelProjection(
	activity *models.ExecutionActivity,
	input workflowRoundInput,
	encodedInput, encodedOutput string,
) bool {
	return activity.ExecutionID == input.Execution.ExecutionID &&
		activity.State == models.ActivityStateCompleted &&
		activity.Name == modelActivityName && activity.OutputJSON != nil &&
		activity.InputJSON == encodedInput &&
		*activity.OutputJSON == encodedOutput
}
