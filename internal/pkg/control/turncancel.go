package control

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
)

// CancelSessionRequest cancels an admitted request, leaving a newer turn and
// children belonging to other turns untouched.
func (r *TurnRouter) CancelSessionRequest(
	ctx context.Context,
	sessionID, requestID uuid.UUID,
) (bool, error) {
	turnID, err := r.registry.store.CancelRequest(ctx, sessionID, requestID)
	if err != nil {
		return false, ctxerrors.Wrap(err, "stop routed request")
	}

	if turnID == uuid.Nil {
		return false, nil
	}

	live, found := r.workers.Current(sessionID)
	if !found {
		return true, nil
	}

	if _, err := live.CancelTurn(ctx, turnID); err != nil {
		return false, ctxerrors.Wrap(err, "interrupt routed request")
	}

	return true, nil
}
