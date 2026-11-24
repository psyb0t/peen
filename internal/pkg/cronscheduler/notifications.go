package cronscheduler

import (
	"context"
	"fmt"

	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

func (s *Scheduler) notify(
	ctx context.Context, run models.CronRun,
	state models.CronRunState, reason string,
) {
	if s.options.Notifier == nil || run.SessionID == nil {
		return
	}

	var chatID int64

	s.mutex.Lock()
	for _, job := range s.jobs {
		if job.definition.Name == run.JobName {
			chatID = job.definition.TelegramChatID

			break
		}
	}
	s.mutex.Unlock()

	if chatID == 0 {
		return
	}

	text := fmt.Sprintf("Scheduled job %s: %s", run.JobName, state)
	if reason != "" {
		text += "\n" + reason
	}

	if state == models.CronRunStateCompleted {
		text = s.completedNotification(ctx, run, text)
	}

	if err := s.options.Notifier.NotifyCron(ctx, control.CronNotification{
		RunID: run.ID, ChatID: chatID, SessionID: *run.SessionID, Text: text,
	}); err != nil {
		ctxscope.GetLogger(ctx).Warn("scheduled notification failed",
			"job_name", run.JobName, "err", err)
	}
}

func (s *Scheduler) completedNotification(
	ctx context.Context, run models.CronRun, fallback string,
) string {
	turn, err := s.options.Turns.GetTurnByRequest(ctx, *run.SessionID, run.ID)
	if err != nil {
		ctxscope.GetLogger(ctx).Warn("scheduled notification turn unavailable",
			"job_name", run.JobName, "reason", "turnUnavailable")

		return fallback
	}

	if turn.Output != nil {
		return fallback + "\n" + *turn.Output
	}

	summary, err := s.options.Turns.GetTurnModelSummary(ctx,
		*run.SessionID, turn.ID)
	if err != nil {
		ctxscope.GetLogger(ctx).Warn("scheduled notification text unavailable",
			"job_name", run.JobName, "reason", "textUnavailable")

		return fallback
	}

	return fallback + "\n" + summary.Text
}
