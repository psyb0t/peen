package agent

import (
	"context"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (r *Runtime) wakeRetainedQuestions(ctx context.Context) error {
	if r.eventBus == nil || r.store.IsActive(r.sessionID) {
		return nil
	}

	options := session.ListSessionNoticesOptions{
		State: models.NoticeStatePending,
	}
	inspectedCount := 0

	for {
		page, err := r.store.ListSessionNotices(ctx, r.sessionID, options)
		if err != nil {
			return ctxerrors.Wrap(err, "read pending child questions")
		}

		for _, notice := range page.Items {
			inspectedCount++

			if !isRetainedQuestionNotice(notice) {
				continue
			}

			started, err := r.wakeRetainedQuestion(ctx, notice)
			if err != nil {
				return ctxerrors.Wrap(err, "offer retained child question")
			}

			if started {
				ctxscope.GetLogger(ctx).Info("retained question wake requested",
					"notice_id", notice.ID, "inspected_count", inspectedCount)

				return nil
			}
		}

		if !page.HasMore {
			ctxscope.GetLogger(ctx).Info("retained questions scanned",
				"inspected_count", inspectedCount, "wake_count", 0)

			return nil
		}

		options.Offset += len(page.Items)
	}
}

func (r *Runtime) wakeRetainedQuestion(
	ctx context.Context,
	notice *models.SessionNotice,
) (bool, error) {
	page, err := r.store.ListSessionNotices(ctx, notice.SessionID,
		session.ListSessionNoticesOptions{
			ID:                 &notice.ID,
			State:              models.NoticeStatePending,
			ValidateWakeSource: true,
		})
	if errors.Is(err, commerr.ErrInvalidState) ||
		errors.Is(err, commerr.ErrConflict) ||
		errors.Is(err, commerr.ErrValidationFailed) ||
		errors.Is(err, commerr.ErrNotFound) {
		ctxscope.GetLogger(ctx).Debug("retained question stays queued",
			"notice_id", notice.ID, "reason", "wake_source_unavailable")

		return false, nil
	}

	if err != nil {
		return false, ctxerrors.Wrap(err,
			"read retained question wake eligibility")
	}

	if len(page.Items) == 0 {
		return false, nil
	}

	batch := sessionNoticeBatch(page.Items)

	return r.considerWake(ctx, batch.Notices[0]), nil
}

func isRetainedQuestionNotice(notice *models.SessionNotice) bool {
	return notice.Type == events.TypeAgentQuestion &&
		notice.Source == events.AgentSource
}

func (r *Runtime) considerPublishedQuestion(
	ctx context.Context,
	notice *events.Notice,
) {
	if notice == nil || r.nativeWorkflowRunner() == nil {
		return
	}

	r.considerWake(ctx, *notice)
}
