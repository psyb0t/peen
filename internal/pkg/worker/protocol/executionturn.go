package protocol

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (s *Store) GetExecutionRootTurn(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
) (*models.Turn, error) {
	return executionCall[*models.Turn](ctx, s, sessionID, executionRequest{
		Operation: executionGetRootTurn, ExecutionID: executionID,
	})
}

func (s *Store) RestoreExecutionTurn(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
	input session.RestoreExecutionTurnInput,
) (session.Lease, error) {
	lease, err := executionCall[session.Lease](ctx, s, sessionID,
		executionRequest{
			Operation:   executionRestoreTurn,
			ExecutionID: executionID, RestoreTurn: input,
		})
	if err != nil {
		return session.Lease{}, ctxerrors.Wrap(err, "restore worker turn lease")
	}

	s.rememberTurnLease(lease)

	return lease, nil
}

func (s *Store) GetExecutionTurnLease(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
) (*models.ExecutionTurnLease, error) {
	return executionCall[*models.ExecutionTurnLease](ctx, s, sessionID,
		executionRequest{
			Operation: executionGetTurnLease, ExecutionID: executionID,
		})
}

func (c *Controller) callExecutionTurn(
	ctx context.Context,
	r executionRequest,
) (any, error) {
	var (
		result any
		err    error
	)

	//nolint:exhaustive // Other execution operations have their own dispatch.
	switch r.Operation {
	case executionReserveCompletion:
		result, err = c.store.ReserveExecutionCompletion(ctx, c.sessionID,
			r.Lease)
	case executionGetRootTurn:
		result, err = c.store.GetExecutionRootTurn(ctx, c.sessionID,
			r.ExecutionID)
	case executionGetTurnLease:
		result, err = c.store.GetExecutionTurnLease(ctx, c.sessionID,
			r.ExecutionID)
	case executionRestoreTurn:
		if r.RestoreTurn.OwnerGenerationID != c.generationID ||
			r.RestoreTurn.WorkerGenerationID != c.generationID {
			return nil, ctxerrors.Wrap(commerr.ErrPermissionDenied,
				"turn holder differs from its connection")
		}

		result, err = c.store.RestoreExecutionTurn(ctx, c.sessionID,
			r.ExecutionID, r.RestoreTurn)
	default:
		return c.callExecutionDeferred(ctx, r)
	}

	if err != nil {
		return nil, ctxerrors.Wrap(err, "execution turn lease operation")
	}

	return result, nil
}
