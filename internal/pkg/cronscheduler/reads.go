package cronscheduler

import (
	"context"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (s *Scheduler) ListCronJobs(ctx context.Context) (api.CronJobList, error) {
	s.mutex.Lock()
	jobs := append([]jobState(nil), s.jobs...)
	s.mutex.Unlock()

	result := api.CronJobList{Items: make([]api.CronJob, 0, len(jobs))}
	for _, job := range jobs {
		item := api.CronJob{
			Name: job.definition.Name, Schedule: job.definition.ScheduleText,
			TimeZone:        job.definition.TimeZone,
			Session:         api.CronJobSession(job.definition.Session),
			NextScheduledAt: job.record.NextScheduledAt.UTC(),
		}

		last, err := s.options.Store.LastRun(ctx, job.definition.Name)
		if err != nil && !errors.Is(err, commerr.ErrNotFound) {
			return api.CronJobList{}, ctxerrors.Wrap(err,
				"read latest cron run")
		}

		if last != nil {
			converted := cronRunToAPI(last)
			item.LastRun = &converted
		}

		result.Items = append(result.Items, item)
	}

	return result, nil
}

func (s *Scheduler) ListCronRuns(
	ctx context.Context,
	params api.ListCronRunsParams,
) (api.CronRunPage, error) {
	name := ""
	if params.Job != nil {
		name = *params.Job
	}

	limit, offset := int32(session.DefaultPageLimit), int32(0)
	if params.Limit != nil {
		limit = *params.Limit
	}

	if params.Offset != nil {
		offset = *params.Offset
	}

	rows, more, err := s.options.Store.ListRuns(ctx, name,
		int(limit), int(offset))
	if err != nil {
		return api.CronRunPage{}, ctxerrors.Wrap(err, "read scheduled history")
	}

	page := api.CronRunPage{
		Items: make([]api.CronRun, 0, len(rows)),
		Limit: limit, Offset: offset, HasMore: more,
	}
	for _, row := range rows {
		page.Items = append(page.Items, cronRunToAPI(row))
	}

	return page, nil
}

func cronRunToAPI(row *models.CronRun) api.CronRun {
	result := api.CronRun{
		Id: row.ID, JobName: row.JobName, ScheduledAt: row.ScheduledAt.UTC(),
		StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
		SessionId: row.SessionID, State: api.CronRunState(row.State),
	}
	if row.Error != "" {
		result.Error = &row.Error
	}

	return result
}
