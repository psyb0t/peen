package session

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

// ListSessionsWithUnfinishedExecutions returns the sessions that have a root or
// child execution in the running or reconciling state, oldest first. Those
// executions belong to a worker that is gone, so a controller that resumes
// work after a crash starts a worker for each of these sessions.
func (s *Store) ListSessionsWithUnfinishedExecutions(
	ctx context.Context,
) ([]*models.Session, error) {
	repository := s.query.Execution

	unfinished, err := repository.WithContext(ctx).Where(
		repository.State.In(
			string(models.ExecutionStateRunning),
			string(models.ExecutionStateReconciling),
		),
	).Order(repository.CreatedAt, repository.ID).Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list unfinished executions")
	}

	seen := make(map[uuid.UUID]struct{}, len(unfinished))
	sessions := make([]*models.Session, 0, len(unfinished))

	for _, execution := range unfinished {
		if _, found := seen[execution.SessionID]; found {
			continue
		}

		seen[execution.SessionID] = struct{}{}

		stored, err := s.findSession(ctx, execution.SessionID)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "read unfinished execution session")
		}

		sessions = append(sessions, stored)
	}

	return sessions, nil
}
