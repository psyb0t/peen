package session

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

// GetExecutionMessageByRequest reads frozen acceptance without waking work.
func (s *Store) GetExecutionMessageByRequest(
	ctx context.Context,
	sessionID, executionID, requestID uuid.UUID,
) (*models.ExecutionInboxMessage, error) {
	if _, err := s.GetExecution(ctx, sessionID, executionID); err != nil {
		return nil, ctxerrors.Wrap(err, "find execution for inbox request")
	}

	repository := s.query.ExecutionInboxMessage

	message, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(sessionID),
		repository.ExecutionID.Eq(executionID),
		repository.RequestID.Eq(requestID),
	).First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read execution inbox request")
	}

	return message, nil
}

// ListExecutionMessages reads retained inbox state without resuming work.
func (s *Store) ListExecutionMessages(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
	options ListExecutionInboxOptions,
) (*ExecutionInboxPage, error) {
	page, err := normalizeReadPage(options.Limit, options.Offset)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "normalize inbox page")
	}

	if options.State != "" && options.State != models.InboxMessageStateQueued &&
		options.State != models.InboxMessageStateDelivered &&
		options.State != models.InboxMessageStateDeferred &&
		options.State != models.InboxMessageStateForwarded {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed, "inbox state")
	}

	if _, err := s.GetExecution(ctx, sessionID, executionID); err != nil {
		return nil, ctxerrors.Wrap(err, "find execution for inbox read")
	}

	repository := s.query.ExecutionInboxMessage

	query := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(sessionID),
		repository.ExecutionID.Eq(executionID),
	).Order(repository.Sequence, repository.ID)
	if options.State != "" {
		query = query.Where(repository.State.Eq(string(options.State)))
	}

	items, hasMore, err := listReadPage(page,
		func(offset, limit int) ([]*models.ExecutionInboxMessage, error) {
			return query.Offset(offset).Limit(limit).Find()
		}, "read execution inbox", "probe inbox page")
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read inbox page")
	}

	return &ExecutionInboxPage{
		Items: items, Limit: page.limit, Offset: page.offset, HasMore: hasMore,
	}, nil
}
