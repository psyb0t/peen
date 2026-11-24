package cronscheduler

import (
	"context"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/cronstore"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func turnOutcome(turn *models.Turn) (models.CronRunState, string) {
	switch turn.State {
	case models.TurnStateCompleted:
		if turn.OutputError != nil && *turn.OutputError != "" {
			return models.CronRunStateFailed, cronstore.ErrorOutputInvalid
		}

		return models.CronRunStateCompleted, ""
	case models.TurnStateCancelled:
		if turn.FailureClassification == session.TurnFailureTimeout {
			return models.CronRunStateCancelled, cronstore.ErrorTurnTimeout
		}

		return models.CronRunStateCancelled, cronstore.ErrorTurnCancelled
	case models.TurnStateInterrupted:
		return models.CronRunStateFailed, cronstore.ErrorInterrupted
	case models.TurnStateFailed, models.TurnStateRunning:
		return models.CronRunStateFailed, cronstore.ErrorTurnFailed
	}

	return models.CronRunStateFailed, cronstore.ErrorRunFailed
}

func (s *Scheduler) recoverRuns(ctx context.Context) error {
	runs, err := s.options.Store.RunningRuns(ctx)
	if err != nil {
		return ctxerrors.Wrap(err, "read unfinished scheduled runs")
	}

	for _, run := range runs {
		if err := s.recoverRun(ctx, *run); err != nil {
			return err
		}
	}

	return nil
}

func (s *Scheduler) recoverRun(ctx context.Context, run models.CronRun) error {
	if run.SessionID == nil {
		s.finish(ctx, run, models.CronRunStateFailed,
			cronstore.ErrorInterrupted)

		return nil
	}

	if err := s.options.Sessions.ValidateWorkspaceSession(ctx,
		*run.SessionID); err != nil {
		if !errors.Is(err, commerr.ErrNotFound) &&
			!errors.Is(err, commerr.ErrPermissionDenied) {
			return ctxerrors.Wrap(err, "validate unfinished scheduled session")
		}

		s.finish(ctx, run, models.CronRunStateFailed,
			cronstore.ErrorWorkspaceUnavailable)

		return nil
	}

	turn, err := s.options.Turns.GetTurnByRequest(ctx, *run.SessionID, run.ID)
	if errors.Is(err, commerr.ErrNotFound) {
		s.finish(ctx, run, models.CronRunStateFailed,
			cronstore.ErrorInterrupted)

		return nil
	}

	if err != nil {
		return ctxerrors.Wrap(err, "read unfinished scheduled turn")
	}

	if turn.State != models.TurnStateRunning {
		state, reason := turnOutcome(turn)
		s.finish(ctx, run, state, reason)

		return nil
	}

	s.mutex.Lock()
	s.active[run.JobName]++
	s.workers.Add(1)

	s.mutex.Unlock()
	go s.watchRun(ctx, run)

	return nil
}

func (s *Scheduler) watchRun(ctx context.Context, run models.CronRun) {
	ctx = ctxscope.Set(ctx, ctxscope.Attr("request_id", run.ID.String()),
		ctxscope.Attr("job_name", run.JobName))

	defer s.releaseJob(run.JobName)
	defer func() {
		if recovered := recover(); recovered != nil {
			ctxscope.GetLogger(ctx).Error("scheduled recovery panicked",
				"job_name", run.JobName, "reason", reasonRecoveryPanicked)
			s.finish(ctx, run, models.CronRunStateFailed,
				cronstore.ErrorRunFailed)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.options.Clock.After(pollInterval):
		}

		turn, err := s.options.Turns.GetTurnByRequest(ctx,
			*run.SessionID, run.ID)
		if err != nil {
			s.finish(ctx, run, models.CronRunStateFailed,
				cronstore.ErrorRunFailed)

			return
		}

		if turn.State == models.TurnStateRunning {
			continue
		}

		state, reason := turnOutcome(turn)
		s.finish(ctx, run, state, reason)

		return
	}
}
