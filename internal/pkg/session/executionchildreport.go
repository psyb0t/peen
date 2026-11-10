package session

import (
	"context"
	"encoding/json"
	"math"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

const emptyChildReportPartsJSON = `[]`

func validateChildReportWithoutOutcome(text string) error {
	if text != "" {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"child report requires a child outcome")
	}

	return nil
}

func (s *Store) recordExecutionChildReport(
	ctx context.Context,
	tx *repositories.Query,
	child *models.Execution,
	run *models.AgentRun,
	event *models.AgentRunEvent,
	text string,
) error {
	if text == "" {
		return nil
	}

	if child.ParentExecutionID == nil {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"reported child has no parent execution")
	}

	parent, err := s.findExecution(ctx, tx, child.SessionID,
		*child.ParentExecutionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read child report parent")
	}

	isNotice, err := s.reportsAsRootNotice(ctx, tx, parent, run)
	if err != nil {
		return err
	}

	if isNotice {
		return s.saveRootChildOutcome(ctx, tx, parent, run, event)
	}

	if parent.EventCount == math.MaxInt64 {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"parent report event sequence is exhausted")
	}

	sequence, err := nextInboxSequence(ctx, tx, parent.ID)
	if err != nil {
		return ctxerrors.Wrap(err, "allocate child report sequence")
	}

	// Child reports and questions survive a full user-message queue.
	message := &models.ExecutionInboxMessage{
		ID: s.newID(), SessionID: parent.SessionID, ExecutionID: parent.ID,
		TurnID: parent.RootTurnID, RequestID: event.ID,
		SourceEventID: &event.ID,
		Sequence:      sequence, WorkingDirectory: parent.WorkingDirectory,
		Message: text, PartsJSON: emptyChildReportPartsJSON,
		ImagesJSON: emptyImagesJSON,
		State:      models.InboxMessageStateQueued, CreatedAt: event.CreatedAt,
	}
	if err := tx.ExecutionInboxMessage.WithContext(ctx).Create(
		message); err != nil {
		return ctxerrors.Wrap(err, "save child completion report")
	}

	return s.recordChildReportEvent(ctx, tx, parent, child, message)
}

// reportsAsRootNotice reports whether a finished child's report goes to its
// root as a session notice: the root can no longer read its inbox.
func (s *Store) reportsAsRootNotice(
	ctx context.Context,
	tx *repositories.Query,
	parent *models.Execution,
	run *models.AgentRun,
) (bool, error) {
	if parent.Kind != models.ExecutionKindRoot ||
		run.State == models.AgentRunStateRunning {
		return false, nil
	}

	closed, err := rootReportsClosed(ctx, tx, parent)
	if err != nil {
		return false, ctxerrors.Wrap(err, "check root report destination")
	}

	return closed, nil
}

func (s *Store) recordChildReportEvent(
	ctx context.Context,
	tx *repositories.Query,
	parent, child *models.Execution,
	message *models.ExecutionInboxMessage,
) error {
	encoded, err := json.Marshal(struct {
		InboxID     uuid.UUID `json:"inboxId"`
		RequestID   uuid.UUID `json:"requestId"`
		ExecutionID uuid.UUID `json:"childExecutionId"`
		AgentRunID  uuid.UUID `json:"agentRunId"`
	}{message.ID, message.RequestID, child.ID, *child.AgentRunID})
	if err != nil {
		return ctxerrors.Wrap(err, "encode child report identity")
	}

	return s.appendExecutionEvent(ctx, tx, parent,
		ExecutionEventTypeInboxQueued, encoded)
}
