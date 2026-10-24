package session

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/events"
)

// isChildBroadcastNotice reports a notice every running child agent reads
// too, not just the root conversation: a turn that failed or was stopped, and
// a child that did not finish. Every agent in the session then knows what went
// wrong, whoever it happened to.
func isChildBroadcastNotice(noticeType string) bool {
	switch noticeType {
	case events.TypeTurnFailed, events.TypeTurnCancelled,
		events.TypeAgentFailed:
		return true
	default:
		return false
	}
}

// broadcastNoticeToChildren queues notice in the inbox of every child that is
// queued or running, in the caller's transaction. A paused child is skipped,
// because a message resumes it, and a finished one never reads again. The
// copies bypass the inbox limit, like a child's report to its parent.
func (s *Store) broadcastNoticeToChildren(
	ctx context.Context,
	tx *repositories.Query,
	notice *models.SessionNotice,
) error {
	if !isChildBroadcastNotice(notice.Type) {
		return nil
	}

	repository := tx.Execution

	children, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(notice.SessionID),
		repository.Kind.Eq(string(models.ExecutionKindChild)),
		repository.State.In(string(models.ExecutionStateQueued),
			string(models.ExecutionStateRunning)),
	).Find()
	if err != nil {
		return ctxerrors.Wrap(err, "read children for notice")
	}

	if len(children) == 0 {
		return nil
	}

	text := events.RenderPrompt(events.Batch{
		Notices: []events.Notice{noticeFromRecord(notice)},
	})

	for _, child := range children {
		if err := s.queueChildNotice(ctx, tx, child, notice,
			text); err != nil {
			return ctxerrors.Wrap(err, "queue notice for child")
		}
	}

	ctxscope.GetLogger(ctx).Info("notice queued for running children",
		"session_id", notice.SessionID, "notice_id", notice.ID,
		"notice_type", notice.Type, "child_count", len(children))

	return nil
}

func (s *Store) queueChildNotice(
	ctx context.Context,
	tx *repositories.Query,
	child *models.Execution,
	notice *models.SessionNotice,
	text string,
) error {
	if child.AgentRunID == nil {
		return ctxerrors.Wrap(commerr.ErrConflict, "child has no agent run")
	}

	sequence, err := nextInboxSequence(ctx, tx, child.ID)
	if err != nil {
		return ctxerrors.Wrap(err, "allocate child notice sequence")
	}

	message := &models.ExecutionInboxMessage{
		ID: s.newID(), SessionID: child.SessionID, ExecutionID: child.ID,
		TurnID:        child.RootTurnID,
		RequestID:     uuid.NewSHA1(notice.ID, child.ID[:]),
		SourceEventID: &notice.ID,
		Sequence:      sequence, WorkingDirectory: child.WorkingDirectory,
		Message: text, PartsJSON: emptyChildReportPartsJSON,
		State: models.InboxMessageStateQueued, CreatedAt: notice.CreatedAt,
	}
	if err := tx.ExecutionInboxMessage.WithContext(ctx).Create(
		message); err != nil {
		return ctxerrors.Wrap(err, "save child notice")
	}

	return s.recordChildInboxAcceptance(ctx, tx, child, message)
}

// noticeFromRecord is the event a stored notice renders as for a model.
func noticeFromRecord(notice *models.SessionNotice) events.Notice {
	return events.Notice{
		ID: notice.ID, SessionID: notice.SessionID, Type: notice.Type,
		Source: notice.Source, Summary: notice.Summary,
		Data:      json.RawMessage(notice.DataJSON),
		Delivery:  string(notice.Delivery),
		CreatedAt: notice.CreatedAt,
	}
}
