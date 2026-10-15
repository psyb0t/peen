package session

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

// SearchExecutions reads every retained state without waking any execution.
// Times form a half-open interval; text is a literal substring, not SQL syntax.
func (s *Store) SearchExecutions(
	ctx context.Context,
	sessionID uuid.UUID,
	options SearchExecutionsOptions,
) (*ExecutionPage, error) {
	page, err := normalizeReadPage(options.Limit, options.Offset)
	if err != nil {
		return nil, err
	}

	if err := validateExecutionSearch(options); err != nil {
		return nil, err
	}

	if _, err := s.findSession(ctx, sessionID); err != nil {
		return nil, ctxerrors.Wrap(err, "find execution search session")
	}

	items, hasMore, err := listReadPage(page,
		func(offset, limit int) ([]*models.Execution, error) {
			return s.query.Execution.WithContext(ctx).Search(
				sessionID, optionalExecutionID(options.ID),
				optionalExecutionID(options.ParentID),
				optionalExecutionID(options.RootID), string(options.State),
				options.Text, executionSearchTime(options.FromTime),
				executionSearchTime(options.ToTime), limit, offset,
			)
		}, "search executions", "probe execution search page",
	)
	if err != nil {
		return nil, err
	}

	return &ExecutionPage{
		Items: items, Limit: page.limit, Offset: page.offset, HasMore: hasMore,
	}, nil
}

//nolint:dupl // Activity and event repositories use distinct typed builders.
func (s *Store) ListExecutionEvents(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
	options ListEventsOptions,
) (*ExecutionEventPage, error) {
	page, err := normalizeReadPage(options.Limit, options.Offset)
	if err != nil {
		return nil, err
	}

	if _, err := s.GetExecution(ctx, sessionID, executionID); err != nil {
		return nil, ctxerrors.Wrap(err, "find execution for history read")
	}

	repository := s.query.ExecutionEvent
	query := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(sessionID),
		repository.ExecutionID.Eq(executionID),
	).Order(repository.Sequence, repository.ID)

	items, hasMore, err := listReadPage(page,
		func(offset, limit int) ([]*models.ExecutionEvent, error) {
			return query.Offset(offset).Limit(limit).Find()
		}, "read execution history", "probe execution history page",
	)
	if err != nil {
		return nil, err
	}

	return &ExecutionEventPage{
		Items: items, Limit: page.limit, Offset: page.offset, HasMore: hasMore,
	}, nil
}

func optionalExecutionID(value uuid.UUID) string {
	if value == uuid.Nil {
		return ""
	}

	return value.String()
}

func executionSearchTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}

	converted := value.UTC()

	return &converted
}

func validateExecutionSearch(options SearchExecutionsOptions) error {
	if options.FromTime != nil && options.ToTime != nil &&
		!options.FromTime.Before(*options.ToTime) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"execution search time interval")
	}

	switch options.State {
	case "", models.ExecutionStateQueued, models.ExecutionStateRunning,
		models.ExecutionStatePaused, models.ExecutionStateReconciling,
		models.ExecutionStateCompleted, models.ExecutionStateFailed,
		models.ExecutionStateCancelled:
		return nil
	}

	return ctxerrors.Wrap(commerr.ErrValidationFailed, "execution search state")
}
