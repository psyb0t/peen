package cronscheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/cronconfig"
	"github.com/psyb0t/peen/internal/pkg/cronstore"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

func New(ctx context.Context, options Options) (*Scheduler, error) {
	if options.Store == nil || options.Runner == nil ||
		options.Sessions == nil || options.Turns == nil {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"cron scheduler dependencies")
	}

	if options.Clock == nil {
		options.Clock = wallClock{}
	}

	if options.MaxMessageBytes <= 0 {
		options.MaxMessageBytes = defaultMaxMessageBytes
	}

	scheduler := &Scheduler{options: options, active: make(map[string]int)}
	for _, job := range options.Jobs {
		if err := scheduler.prepareJob(ctx, job); err != nil {
			return nil, ctxerrors.Wrapf(err, "prepare cron job %q", job.Name)
		}
	}

	sort.Slice(scheduler.jobs, func(first, second int) bool {
		return scheduler.jobs[first].definition.Name <
			scheduler.jobs[second].definition.Name
	})

	if err := scheduler.checkAgentSpaces(ctx); err != nil {
		return nil, err
	}

	return scheduler, nil
}

func (s *Scheduler) prepareJob(ctx context.Context, job cronconfig.Job) error {
	if job.Schedule == nil {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"schedule is missing")
	}

	canonical, err := s.options.Sessions.ResolveWorkspace(job.Workspace)
	if err != nil {
		return ctxerrors.Wrap(err, "validate cron workspace")
	}

	job.Workspace = canonical
	job.OutputSchema = append([]byte(nil), job.OutputSchema...)

	hash, err := definitionHash(job)
	if err != nil {
		return err
	}

	record, err := s.scheduleBaseline(ctx, job, hash)
	if err != nil {
		return err
	}

	if err := s.options.Store.SaveJob(ctx, record); err != nil {
		return ctxerrors.Wrap(err, "save cron schedule baseline")
	}

	s.jobs = append(s.jobs, jobState{definition: job, record: *record})

	return nil
}

func (s *Scheduler) scheduleBaseline(
	ctx context.Context,
	job cronconfig.Job,
	hash string,
) (*models.CronJob, error) {
	record, err := s.options.Store.GetJob(ctx, job.Name)
	if err != nil && !errors.Is(err, commerr.ErrNotFound) {
		return nil, ctxerrors.Wrap(err, "read cron schedule baseline")
	}

	now := s.options.Clock.Now().UTC()
	if record == nil || record.DefinitionHash != hash {
		record = &models.CronJob{
			JobName: job.Name, DefinitionHash: hash,
			NextScheduledAt: job.Schedule.Next(now).UTC(),
		}
	}

	if record.NextScheduledAt.IsZero() {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"schedule has no next occurrence")
	}

	if !record.NextScheduledAt.After(now) {
		missed := latestOccurrence(job.Schedule, record.NextScheduledAt, now)
		if err := s.recordSkipped(ctx, job.Name, missed, now,
			cronstore.ErrorControllerOffline); err != nil {
			return nil, err
		}

		record.NextScheduledAt = job.Schedule.Next(missed).UTC()
	}

	return record, nil
}

func definitionHash(job cronconfig.Job) (string, error) {
	encoded, err := json.Marshal([]any{
		job.Name, job.ScheduleText, job.TimeZone, job.Workspace,
		job.AgentSpace, job.Session, job.Message, job.Model,
		job.ReasoningEffort, job.Timeout, job.OutputSchema,
		job.TelegramChatID,
	})
	if err != nil {
		return "", ctxerrors.Wrap(err, "hash cron definition")
	}

	digest := sha256.Sum256(encoded)

	return hex.EncodeToString(digest[:]), nil
}

func (s *Scheduler) Run(ctx context.Context) error {
	s.mutex.Lock()
	if s.closed || s.started {
		s.mutex.Unlock()

		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"cron scheduler already started or closed")
	}

	ctx, s.cancel = context.WithCancel(ctx)
	s.started = true
	s.workers.Add(1)

	s.mutex.Unlock()
	defer s.workers.Done()
	defer s.cancel()

	if err := s.recoverRuns(ctx); err != nil {
		return ctxerrors.Wrap(err, "reconcile scheduled runs")
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.options.Clock.After(pollInterval):
			if err := s.step(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}

				return ctxerrors.Wrap(err, "dispatch scheduled occurrences")
			}
		}
	}
}

// Close waits for dispatch callers to leave before the core closes SQLite.
// Durable turns keep the controller's normal shutdown and recovery behavior.
func (s *Scheduler) Close() {
	s.mutex.Lock()

	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	s.mutex.Unlock()
	s.workers.Wait()
}

func (s *Scheduler) step(ctx context.Context) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.closed || ctx.Err() != nil {
		return nil
	}

	now := s.options.Clock.Now().UTC()
	for index := range s.jobs {
		job := &s.jobs[index]
		if job.record.NextScheduledAt.After(now) {
			continue
		}

		scheduled := latestOccurrence(job.definition.Schedule,
			job.record.NextScheduledAt, now)

		next := job.definition.Schedule.Next(scheduled).UTC()
		if next.IsZero() {
			return ctxerrors.Wrap(commerr.ErrInvalidState,
				"cron schedule exhausted")
		}

		if err := s.fire(ctx, job, scheduled, now); err != nil {
			return err
		}

		job.record.NextScheduledAt = next
		if err := s.options.Store.SaveJob(ctx, &job.record); err != nil {
			return ctxerrors.Wrap(err, "advance cron schedule")
		}
	}

	return nil
}
