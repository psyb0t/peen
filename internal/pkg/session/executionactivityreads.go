package session

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

func (s *Store) GetExecutionActivity(
	ctx context.Context,
	sessionID, activityID uuid.UUID,
) (*models.ExecutionActivity, error) {
	return s.findExecutionActivity(ctx, s.query, sessionID, activityID)
}

//nolint:dupl // Activity and event repositories use distinct typed builders.
func (s *Store) ListExecutionActivities(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
	options ListEventsOptions,
) (*ExecutionActivityPage, error) {
	page, err := normalizeReadPage(options.Limit, options.Offset)
	if err != nil {
		return nil, err
	}

	if _, err := s.GetExecution(ctx, sessionID, executionID); err != nil {
		return nil, ctxerrors.Wrap(err, "find execution for activity read")
	}

	repository := s.query.ExecutionActivity
	query := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(sessionID),
		repository.ExecutionID.Eq(executionID),
	).Order(repository.CreatedAt, repository.ID)

	items, hasMore, err := listReadPage(page,
		func(offset, limit int) ([]*models.ExecutionActivity, error) {
			return query.Offset(offset).Limit(limit).Find()
		}, "read execution activities", "probe execution activity page",
	)
	if err != nil {
		return nil, err
	}

	return &ExecutionActivityPage{
		Items: items, Limit: page.limit, Offset: page.offset, HasMore: hasMore,
	}, nil
}
