package session

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
)

// GetDirectoryState returns paired tool metadata without transferring history.
func (s *Store) GetDirectoryState(
	ctx context.Context,
	sessionID uuid.UUID,
) (*DirectoryState, error) {
	if _, err := s.findSession(ctx, sessionID); err != nil {
		return nil, ctxerrors.Wrap(err, "find session for directory recovery")
	}

	rows, err := s.query.Message.WithContext(ctx).
		LatestDirectoryChange(sessionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "query latest accepted directory")
	}

	state := &DirectoryState{}
	if len(rows) > 0 {
		state.Directory = rows[0].Workspace
		state.Sequence = rows[0].Sequence
	}

	return state, nil
}
