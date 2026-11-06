package session

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

// CountQueuedMessages counts accepted messages still waiting for delivery, in
// all sessions.
func (s *Store) CountQueuedMessages(ctx context.Context) (int64, error) {
	repository := s.query.ExecutionInboxMessage

	count, err := repository.WithContext(ctx).
		Where(repository.State.Eq(string(models.InboxMessageStateQueued))).
		Count()
	if err != nil {
		return 0, ctxerrors.Wrap(err, "count queued messages")
	}

	return count, nil
}
