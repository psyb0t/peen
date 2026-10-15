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
		State: models.InboxMessageStateQueued, CreatedAt: event.CreatedAt,
	}
	if err := tx.ExecutionInboxMessage.WithContext(ctx).Create(
		message); err != nil {
		return ctxerrors.Wrap(err, "save child completion report")
	}

	return s.recordChildReportEvent(ctx, tx, parent, child, message)
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
