package protocol

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// CancelTurn interrupts only the named turn, after the controller stopped it.
func (w *Worker) CancelTurn(
	ctx context.Context,
	turnID uuid.UUID,
) (bool, error) {
	if turnID == uuid.Nil {
		return false, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"turn cancellation requires a turn ID")
	}

	payload, err := w.conn.Call(ctx, KindCommand, string(CommandCancelTurn),
		CancelTurn{TurnID: turnID})
	if err != nil {
		return false, ctxerrors.Wrap(err, "interrupt worker turn")
	}

	var result CancelResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return false, ctxerrors.Wrap(err, "decode turn cancellation")
	}

	return result.CancelRequested, nil
}

// RequestLocalTurnCancellation leaves a replacement turn untouched.
func (s *Store) RequestLocalTurnCancellation(
	sessionID, turnID uuid.UUID,
) bool {
	if turnID == uuid.Nil {
		return false
	}

	return s.requestLocalCancellation(sessionID, turnID)
}
