// Package cronstore persists schedule baselines and idempotent run claims.
// Only controller-owned code can open or change these rows.
package cronstore

import (
	"context"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

// Store owns typed queries for cron history and schedule baselines.
type Store struct {
	query *repositories.Query
}

func New(handle *db.Handle) (*Store, error) {
	if handle == nil || handle.GormDB == nil {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"cron store requires a database")
	}

	return &Store{query: repositories.Use(handle.GormDB)}, nil
}

func (s *Store) GetJob(
	ctx context.Context, name string,
) (*models.CronJob, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}

	repo := s.query.CronJob

	job, err := repo.WithContext(ctx).Where(repo.JobName.Eq(name)).First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "load cron job")
	}

	return job, nil
}

// SaveJob writes the schedule baseline and definition hash.
func (s *Store) SaveJob(ctx context.Context, job *models.CronJob) error {
	if err := validateJob(job); err != nil {
		return err
	}

	stored := *job

	stored.NextScheduledAt = stored.NextScheduledAt.UTC()
	if err := s.query.CronJob.WithContext(ctx).Save(&stored); err != nil {
		return ctxerrors.Wrap(err, "save cron job")
	}

	return nil
}

// ClaimRun returns false for an occurrence another loop already recorded.
func (s *Store) ClaimRun(
	ctx context.Context, run *models.CronRun,
) (bool, error) {
	if err := validateRun(run); err != nil {
		return false, err
	}

	stored := *run
	stored.ScheduledAt = stored.ScheduledAt.UTC()
	stored.StartedAt = utcTime(stored.StartedAt)

	stored.FinishedAt = utcTime(stored.FinishedAt)
	if err := s.query.CronRun.WithContext(ctx).Create(&stored); err != nil {
		wrapped := ctxerrors.Wrap(err, "claim cron run")
		if errors.Is(wrapped, commerr.ErrAlreadyExists) {
			return false, nil
		}

		return false, wrapped
	}

	return true, nil
}
