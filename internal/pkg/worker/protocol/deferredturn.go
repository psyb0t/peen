package protocol

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (s *Store) AcquireDeferredExecutionTurn(
	ctx context.Context,
	sessionID, inboxID uuid.UUID,
	input session.StartTurnInput,
) (*session.DeferredTurnAdmission, error) {
	result, err := executionCall[*session.DeferredTurnAdmission](ctx, s,
		sessionID, executionRequest{
			Operation: executionAcquireDeferredTurn,
			InboxID:   inboxID, StartTurn: input,
		})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "acquire deferred worker turn")
	}

	s.rememberTurnLease(result.Lease)

	return result, nil
}

func (c *Controller) acquireDeferredTurn(
	ctx context.Context,
	r executionRequest,
) (*session.DeferredTurnAdmission, error) {
	r.StartTurn.WorkerGenerationID = c.generationID.String()

	result, err := c.store.AcquireDeferredExecutionTurn(ctx, c.sessionID,
		r.InboxID, r.StartTurn)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "admit deferred worker turn")
	}

	c.publish(ctx, result.Events)

	return result, nil
}
