package session

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

// GetTurnByRequest reads a request's turn only within the named session.
func (s *Store) GetTurnByRequest(
	ctx context.Context,
	sessionID, requestID uuid.UUID,
) (*models.Turn, error) {
	return findRequestTurn(ctx, s.query, sessionID, requestID)
}

func findRequestTurn(
	ctx context.Context,
	query *repositories.Query,
	sessionID, requestID uuid.UUID,
) (*models.Turn, error) {
	repository := query.Turn

	turn, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(sessionID),
		repository.RequestID.Eq(requestID),
	).First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "find request turn")
	}

	return turn, nil
}

// CancelRequest stops only this admitted request's turn. Another request and
// an unknown or terminal request remain untouched. Callers await admission or
// the worker's answer before using it, so no late admission escapes the stop.
// The returned turn ID is the only worker turn the caller may interrupt.
func (s *Store) CancelRequest(
	ctx context.Context,
	sessionID, requestID uuid.UUID,
) (uuid.UUID, error) {
	if sessionID == uuid.Nil || requestID == uuid.Nil {
		return uuid.Nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"request cancellation requires session and request IDs")
	}

	s.activeMu.Lock()
	active, held := s.active[sessionID]

	var turnID uuid.UUID

	err := s.query.Transaction(func(tx *repositories.Query) error {
		var err error

		turnID, err = s.cancelRequestWithQuery(ctx, tx, sessionID,
			requestID, active, held)

		return err
	})
	s.activeMu.Unlock()

	if err != nil {
		return uuid.Nil, ctxerrors.Wrap(err, "cancel request turn")
	}

	if turnID != uuid.Nil {
		cancel, shouldCall := s.markCancelRequested(sessionID, turnID)
		if shouldCall && cancel != nil {
			cancel()
		}
	}

	return turnID, nil
}

func (s *Store) cancelRequestWithQuery(
	ctx context.Context,
	query *repositories.Query,
	sessionID, requestID uuid.UUID,
	active activeTurn,
	held bool,
) (uuid.UUID, error) {
	turn, err := findRequestTurn(ctx, query, sessionID, requestID)
	if errors.Is(err, commerr.ErrNotFound) {
		return uuid.Nil, nil
	}

	if err != nil {
		return uuid.Nil, ctxerrors.Wrap(err, "read cancellation target")
	}

	if !cancellableRequestTurn(turn, active, held) {
		return uuid.Nil, nil
	}

	repository := query.Turn

	_, err = repository.WithContext(ctx).Where(
		repository.SessionID.Eq(sessionID), repository.ID.Eq(turn.ID),
		repository.State.Eq(string(models.TurnStateRunning)),
	).UpdateSimple(repository.CancelRequested.Value(true))
	if err != nil {
		return uuid.Nil, ctxerrors.Wrap(err, "record request cancellation")
	}

	if _, err := s.pauseTurnExecutions(ctx, query, sessionID,
		turn.ID); err != nil {
		return uuid.Nil, ctxerrors.Wrap(err, "pause request executions")
	}

	return turn.ID, nil
}

func cancellableRequestTurn(
	turn *models.Turn,
	active activeTurn,
	held bool,
) bool {
	return turn != nil && turn.State == models.TurnStateRunning &&
		(!held || active.turnID == turn.ID)
}
