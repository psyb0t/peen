package session

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

// ListDeferredExecutionMessages reads pending input from completed roots.
// Reading never claims input, resumes a source or starts a turn.
func (s *Store) ListDeferredExecutionMessages(
	ctx context.Context,
	sessionID uuid.UUID,
	options ListEventsOptions,
) (*ExecutionInboxPage, error) {
	page, err := normalizeReadPage(options.Limit, options.Offset)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "normalize deferred inbox page")
	}

	if _, err := s.findSession(ctx, sessionID); err != nil {
		return nil, ctxerrors.Wrap(err, "find deferred inbox session")
	}

	items, hasMore, err := listReadPage(page,
		func(offset, limit int) ([]*models.ExecutionInboxMessage, error) {
			return s.query.ExecutionInboxMessage.WithContext(ctx).Deferred(
				sessionID, string(models.InboxMessageStateDeferred),
				string(models.ExecutionStateCompleted),
				string(models.ExecutionKindRoot), limit, offset)
		}, "read deferred inbox", "probe deferred inbox page")
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list deferred execution messages")
	}

	return &ExecutionInboxPage{
		Items: items, Limit: page.limit, Offset: page.offset, HasMore: hasMore,
	}, nil
}
