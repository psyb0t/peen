package run

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/worker/protocol"
)

func (h *commandHandler) cancelExactTurn(
	ctx context.Context,
	payload json.RawMessage,
) (any, error) {
	var request protocol.CancelTurn
	if err := json.Unmarshal(payload, &request); err != nil {
		return nil, ctxerrors.Wrap(err, "decode exact turn cancellation")
	}

	if request.TurnID == uuid.Nil {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"turn cancellation requires a turn ID")
	}

	sessionID := h.runtime.SessionID()
	local := h.store.RequestLocalTurnCancellation(sessionID, request.TurnID)
	interruptErr := h.runtime.InterruptTurn(ctx, request.TurnID)

	ended, endErr := h.store.EndRunningTurn(ctx, sessionID, request.TurnID,
		session.CancelledTurnEnd())
	if err := errors.Join(interruptErr, endErr); err != nil {
		return nil, ctxerrors.Wrap(err, "interrupt exact worker turn")
	}

	return protocol.CancelResult{CancelRequested: local || ended}, nil
}
