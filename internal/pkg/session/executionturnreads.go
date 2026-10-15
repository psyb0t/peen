package session

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

// GetExecutionRootTurn reads the original turn without claiming or resuming it.
func (s *Store) GetExecutionRootTurn(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
) (*models.Turn, error) {
	execution, err := s.findExecution(ctx, s.query, sessionID, executionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "find root execution for turn read")
	}

	if execution.Kind != models.ExecutionKindRoot {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"original turn read requires a root execution")
	}

	repository := s.query.Turn

	turn, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(sessionID),
		repository.ID.Eq(execution.RootTurnID),
	).First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read original execution turn")
	}

	return turn, nil
}
