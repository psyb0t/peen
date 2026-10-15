package agent

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (c *rootWorkflowCallbacks) rootModelProjectionTurn(
	ctx context.Context,
	shared *rootCallbackScope,
	input workflowRoundInput,
) (*preparedTurn, uuid.UUID, error) {
	store, ok := c.runtime.store.(session.ExecutionStorage)
	if !ok {
		return nil, uuid.Nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"root model projection storage")
	}

	_, err := store.GetExecution(ctx, input.Execution.SessionID,
		input.Execution.ExecutionID)
	if err != nil {
		return nil, uuid.Nil, ctxerrors.Wrap(err,
			"read root model execution")
	}

	value := ctx.Value(workflowActivityLeaseContextKey{})

	lease, ok := value.(session.ActivityLease)
	if !ok || lease.GenerationID != c.generationID {
		return nil, uuid.Nil, ctxerrors.Wrap(commerr.ErrConflict,
			"root model projection activity owner changed")
	}

	assistant := *shared.prepared
	assistant.turn = newRootPhaseTurn(shared.prepared.turn)

	return &assistant, lease.ID, nil
}

func (c *rootWorkflowCallbacks) afterModelRound(
	ctx context.Context,
	input workflowRoundInput,
	result workflowRoundResult,
) error {
	projection := result.RootProjection
	if projection == nil {
		return nil
	}

	shared, release, err := c.acquire(ctx, input.Execution, true)
	if err != nil {
		return ctxerrors.Wrap(err, "admit committed root model")
	}
	defer release()

	if err := validateCommittedModelRound(ctx, c.runtime, input, result,
		projection.ActivityID); err != nil {
		return ctxerrors.Wrap(err, "validate committed root model")
	}

	turn := shared.prepared.turn

	release()

	return shared.deliverRootPhaseProjection(ctx, turn, projection)
}
