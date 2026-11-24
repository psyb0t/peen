package cronstore

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (s *Store) AttachSession(
	ctx context.Context, runID, sessionID uuid.UUID,
) error {
	if runID == uuid.Nil || sessionID == uuid.Nil {
		return invalidInput()
	}

	repo := s.query.CronRun

	result, err := repo.WithContext(ctx).Where(
		repo.ID.Eq(runID), repo.State.Eq(string(models.CronRunStateRunning)),
	).Where(repo.SessionID.IsNull()).Or(
		repo.WithContext(ctx).Where(repo.ID.Eq(runID),
			repo.State.Eq(string(models.CronRunStateRunning)),
			repo.SessionID.Eq(sessionID)),
	).UpdateSimple(repo.SessionID.Value(sessionID))
	if err != nil {
		return ctxerrors.Wrap(err, "attach cron run session")
	}

	if result.RowsAffected == 0 {
		return ctxerrors.Wrap(commerr.ErrConflict, "cron run session is fixed")
	}

	return nil
}

func (s *Store) FinishRun(
	ctx context.Context, runID uuid.UUID, state models.CronRunState,
	errorText string, finishedAt time.Time,
) error {
	if runID == uuid.Nil || !terminalState(state) ||
		!validError(errorText) || finishedAt.IsZero() {
		return invalidInput()
	}

	repo := s.query.CronRun

	current, err := repo.WithContext(ctx).Where(repo.ID.Eq(runID)).First()
	if err != nil {
		return ctxerrors.Wrap(err, "load cron run to finish")
	}

	if current.State != models.CronRunStateRunning {
		return nil
	}

	if current.StartedAt == nil || finishedAt.Before(*current.StartedAt) {
		return invalidInput()
	}

	_, err = repo.WithContext(ctx).Where(repo.ID.Eq(runID),
		repo.State.Eq(string(models.CronRunStateRunning))).UpdateSimple(
		repo.State.Value(string(state)), repo.Error.Value(errorText),
		repo.FinishedAt.Value(finishedAt.UTC()),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "finish cron run")
	}

	return nil
}

func (s *Store) ListRuns(
	ctx context.Context, name string, limit, offset int,
) ([]*models.CronRun, bool, error) {
	if name != "" {
		if err := validateName(name); err != nil {
			return nil, false, err
		}
	}

	if limit == 0 {
		limit = session.DefaultPageLimit
	}

	if limit < 1 || limit > session.MaximumPageLimit || offset < 0 {
		return nil, false, invalidInput()
	}

	repo := s.query.CronRun

	query := repo.WithContext(ctx)
	if name != "" {
		query = query.Where(repo.JobName.Eq(name))
	}

	rows, err := query.Order(repo.ScheduledAt.Desc(), repo.ID.Desc()).
		Limit(limit + 1).Offset(offset).Find()
	if err != nil {
		return nil, false, ctxerrors.Wrap(err, "list cron runs")
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	return rows, hasMore, nil
}

func (s *Store) LastRun(
	ctx context.Context, name string,
) (*models.CronRun, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}

	repo := s.query.CronRun

	row, err := repo.WithContext(ctx).Where(repo.JobName.Eq(name)).
		Order(repo.ScheduledAt.Desc(), repo.ID.Desc()).First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "load latest cron run")
	}

	return row, nil
}

func (s *Store) RunningRuns(ctx context.Context) ([]*models.CronRun, error) {
	repo := s.query.CronRun

	rows, err := repo.WithContext(ctx).
		Where(repo.State.Eq(string(models.CronRunStateRunning))).
		Order(repo.ScheduledAt, repo.ID).Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list running cron runs")
	}

	return rows, nil
}
