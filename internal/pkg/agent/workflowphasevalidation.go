package agent

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (r *Runtime) validateWorkflowPhaseActivity(
	ctx context.Context,
	input workflowToolPhaseInput,
	generationID uuid.UUID,
) (session.ActivityLease, error) {
	if err := input.validate(); err != nil {
		return session.ActivityLease{}, ctxerrors.Wrap(err,
			"validate tool phase envelope")
	}

	value := ctx.Value(workflowActivityLeaseContextKey{})

	lease, ok := value.(session.ActivityLease)
	if !ok || generationID == uuid.Nil {
		return lease, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"tool phase lease and generation")
	}

	if lease.GenerationID != generationID {
		return lease, ctxerrors.Wrap(commerr.ErrConflict,
			"tool phase generation changed")
	}

	store, ok := r.store.(session.ExecutionStorage)
	if !ok {
		return lease, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"tool phase storage")
	}

	activity, err := store.GetExecutionActivity(ctx,
		input.Tool.Round.Execution.SessionID, lease.ID)
	if err != nil {
		return lease, ctxerrors.Wrap(err, "read tool phase claim")
	}

	encoded, err := json.Marshal(input)
	if err != nil {
		return lease, ctxerrors.Wrap(err, "encode tool phase claim")
	}

	if !sameWorkflowPhaseClaim(activity, lease, input, encoded) {
		return lease, ctxerrors.Wrap(commerr.ErrConflict,
			"tool phase claim or saved envelope differs")
	}

	return lease, nil
}

func sameWorkflowPhaseClaim(
	activity *models.ExecutionActivity,
	lease session.ActivityLease,
	input workflowToolPhaseInput,
	encoded json.RawMessage,
) bool {
	return activity.Name == toolPhaseActivityName &&
		activity.SessionID == input.Tool.Round.Execution.SessionID &&
		sameChildToolActivity(activity, lease, input.Tool, encoded)
}

func workflowPhaseCallEvent(input workflowToolInput) elelem.ToolCallEvent {
	return elelem.ToolCallEvent{
		CallID: input.Call.ID, Name: input.Call.Name,
		Arguments: input.Call.Arguments, Index: input.Index,
	}
}
