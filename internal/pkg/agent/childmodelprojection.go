package agent

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (c *childWorkflowCallbacks) childModelProjectionSink(
	ctx context.Context,
	shared *childCallbackScope,
	input workflowRoundInput,
) (*agentRunSink, error) {
	store, ok := c.runtime.store.(session.ExecutionStorage)
	if !ok {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"child model projection storage")
	}

	_, err := store.GetExecution(ctx, input.Execution.SessionID,
		input.Execution.ExecutionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read child model execution")
	}

	value := ctx.Value(workflowActivityLeaseContextKey{})

	lease, ok := value.(session.ActivityLease)
	if !ok || lease.GenerationID != c.generationID {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"child model projection activity owner changed")
	}

	return newChildPhaseSink(ctx, shared.actor.sink, lease.ID, shared.revision)
}

func (c *childWorkflowCallbacks) afterModelRound(
	ctx context.Context,
	input workflowRoundInput,
	result workflowRoundResult,
) error {
	projection := result.ChildProjection
	if projection == nil {
		return nil
	}

	shared, admitted, release, err := c.acquire(ctx, input, true)
	if err != nil {
		return ctxerrors.Wrap(err, "admit committed child model")
	}
	defer release()

	if err := validateCommittedModelRound(admitted, c.runtime, input,
		result, projection.ActivityID); err != nil {
		return ctxerrors.Wrap(err, "validate committed child model")
	}

	events, err := c.readChildPhaseEvents(admitted, shared.actor.run,
		projection)
	if err != nil {
		return ctxerrors.Wrap(err, "read committed child model events")
	}

	return c.deliverChildPhaseEvents(admitted, shared, projection.ActivityID,
		events)
}
