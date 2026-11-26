package cronscheduler

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/cronconfig"
	"github.com/psyb0t/peen/internal/pkg/cronstore"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (s *Scheduler) recordSkipped(
	ctx context.Context,
	name string,
	scheduled, now time.Time,
	reason string,
) error {
	_, err := s.options.Store.ClaimRun(ctx, &models.CronRun{
		ID: uuid.New(), JobName: name, ScheduledAt: scheduled,
		FinishedAt: &now, State: models.CronRunStateSkipped, Error: reason,
	})
	if err != nil {
		return ctxerrors.Wrap(err, "record skipped cron occurrence")
	}

	ctxscope.GetLogger(ctx).Info("scheduled occurrence skipped",
		"job_name", name, "reason", reason)

	return nil
}

// fire runs under mutex, so claiming and marking a job active precede the
// next tick. The unique occurrence index also protects a replayed baseline.
func (s *Scheduler) fire(
	ctx context.Context,
	job *jobState,
	scheduled, now time.Time,
) error {
	if s.active[job.definition.Name] > 0 {
		return s.recordSkipped(ctx, job.definition.Name, scheduled, now,
			cronstore.ErrorPreviousRunning)
	}

	run := models.CronRun{
		ID: uuid.New(), JobName: job.definition.Name, ScheduledAt: scheduled,
		StartedAt: &now, State: models.CronRunStateRunning,
	}

	claimed, err := s.options.Store.ClaimRun(ctx, &run)
	if err != nil {
		return ctxerrors.Wrap(err, "claim scheduled occurrence")
	}

	if !claimed {
		return nil
	}

	s.active[run.JobName]++

	s.workers.Add(1)
	go s.execute(ctx, job.definition, run)

	return nil
}

func (s *Scheduler) releaseJob(name string) {
	s.mutex.Lock()
	s.active[name]--
	s.mutex.Unlock()
	s.workers.Done()
}

func (s *Scheduler) execute(
	ctx context.Context,
	job cronconfig.Job,
	run models.CronRun,
) {
	defer s.releaseJob(job.Name)
	defer func() {
		if recovered := recover(); recovered != nil {
			ctxscope.GetLogger(ctx).Error("scheduled run panicked",
				"job_name", job.Name, "reason", reasonRunPanicked)
			s.finish(ctx, run, models.CronRunStateFailed,
				cronstore.ErrorRunFailed)
		}
	}()

	ctx = ctxscope.Set(ctx, ctxscope.Attr("request_id", run.ID.String()),
		ctxscope.Attr("job_name", job.Name))
	ctxscope.GetLogger(ctx).Info("scheduled run started")

	opened, err := s.options.Sessions.Open(ctx, job.Workspace, job.AgentSpace)
	if err != nil {
		s.finish(ctx, run, models.CronRunStateFailed, sessionFailure(err))

		return
	}

	sessionID := opened.Session.ID
	if err := s.options.Store.AttachSession(ctx, run.ID,
		sessionID); err != nil {
		s.finish(ctx, run, models.CronRunStateFailed,
			cronstore.ErrorRunFailed)

		return
	}

	run.SessionID = &sessionID

	message := scheduledMessage(job, run.ScheduledAt)
	if len(message.Message) > s.options.MaxMessageBytes {
		s.finish(ctx, run, models.CronRunStateFailed,
			cronstore.ErrorRunFailed)

		return
	}

	result, err := s.options.Runner.Run(ctx, control.ConversationRequest{
		RequestID: run.ID, SessionID: sessionID, Message: message,
	})
	if ctx.Err() != nil {
		return
	}

	state, reason := s.resultState(ctx, run, result, err)
	s.finish(ctx, run, state, reason)
}

func scheduledMessage(
	job cronconfig.Job,
	scheduled time.Time,
) agent.MessageRequest {
	message := agent.MessageRequest{
		Message: strings.NewReplacer(
			"{scheduledAt}", scheduled.UTC().Format(time.RFC3339),
			"{jobName}", job.Name,
		).Replace(job.Message),
		OutputSchema:   job.OutputSchema,
		TimeoutSeconds: int(job.Timeout / time.Second),
	}
	if job.Model != "" {
		message.Model = &job.Model
	}

	if job.ReasoningEffort != "" {
		message.ReasoningEffort = &job.ReasoningEffort
	}

	return message
}

// sessionFailure is the fixed reason a run records when its session cannot
// be prepared.
func sessionFailure(err error) string {
	var conflict *session.AgentSpaceConflictError
	if errors.As(err, &conflict) {
		return cronstore.ErrorAgentSpaceConflict
	}

	return cronstore.ErrorSessionUnavailable
}

func (s *Scheduler) resultState(
	ctx context.Context,
	run models.CronRun,
	result *control.ConversationResult,
	runErr error,
) (models.CronRunState, string) {
	if errors.Is(runErr, session.ErrSessionBusy) ||
		errors.Is(runErr, commerr.ErrLockHeld) {
		return models.CronRunStateSkipped, cronstore.ErrorWorkspaceBusy
	}

	if result != nil && runErr == nil {
		if result.OutputError != "" {
			return models.CronRunStateFailed, cronstore.ErrorOutputInvalid
		}

		return turnOutcome(&models.Turn{State: models.TurnState(result.State)})
	}

	turn, err := s.options.Turns.GetTurnByRequest(ctx, *run.SessionID, run.ID)
	if err != nil {
		return models.CronRunStateFailed, cronstore.ErrorRunFailed
	}

	return turnOutcome(turn)
}

func (s *Scheduler) finish(
	ctx context.Context,
	run models.CronRun,
	state models.CronRunState,
	reason string,
) {
	if ctx.Err() != nil {
		return
	}

	now := s.options.Clock.Now().UTC()
	if err := s.options.Store.FinishRun(ctx, run.ID, state, reason,
		now); err != nil {
		ctxscope.GetLogger(ctx).Error("recording scheduled result failed",
			"job_name", run.JobName, "err", err)

		return
	}

	ctxscope.GetLogger(ctx).Info("scheduled run finished",
		"job_name", run.JobName, "state", state, "reason", reason)
	s.notify(ctx, run, state, reason)
}
