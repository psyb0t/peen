package session

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

// childInboxEventPayload is the event of an agent.run.message.* step. A
// durable child's inbox row is identified by its request ID, which links the
// message's queued step to its delivered or undelivered one.
type childInboxEventPayload struct {
	Message      string    `json:"message"`
	AcceptanceID uuid.UUID `json:"acceptanceId"`
}

// recordChildInboxRunEvent stores the agent.run.message.* step kind for one
// inbox row of a child execution, in the caller's feed transaction, so the
// step reaches the session feed with the inbox change it reports. An
// execution with no agent run, such as a root, records nothing.
func (s *Store) recordChildInboxRunEvent(
	ctx context.Context,
	tx *repositories.Query,
	execution *models.Execution,
	kind string,
	row *models.ExecutionInboxMessage,
) error {
	if execution.Kind != models.ExecutionKindChild ||
		execution.AgentRunID == nil {
		return nil
	}

	run, err := s.findAgentRunWithQuery(ctx, tx, execution.SessionID,
		*execution.AgentRunID)
	if err != nil {
		return ctxerrors.Wrap(err, "read inbox event agent run")
	}

	payload, err := json.Marshal(childInboxEventPayload{
		Message: row.Message, AcceptanceID: row.RequestID,
	})
	if err != nil {
		return ctxerrors.Wrap(err, "encode child inbox event")
	}

	if _, err := s.appendExecutionChildEvent(ctx, tx, run, kind,
		payload); err != nil {
		return ctxerrors.Wrap(err, "save child inbox event")
	}

	return nil
}

// recordUndeliveredChildInbox stores agent.run.message.undelivered for every
// message still queued for a child that is about to end. Nothing reads a
// finished child's inbox, so these messages never reach its model.
func (s *Store) recordUndeliveredChildInbox(
	ctx context.Context,
	tx *repositories.Query,
	execution *models.Execution,
) error {
	inbox := tx.ExecutionInboxMessage

	rows, err := inbox.WithContext(ctx).Where(
		inbox.SessionID.Eq(execution.SessionID),
		inbox.ExecutionID.Eq(execution.ID),
		inbox.State.Eq(string(models.InboxMessageStateQueued)),
	).Order(inbox.Sequence).Find()
	if err != nil {
		return ctxerrors.Wrap(err, "read unread child inbox")
	}

	for _, row := range rows {
		if err := s.recordChildInboxRunEvent(ctx, tx, execution,
			EventTypeAgentRunMessageUndelivered, row); err != nil {
			return ctxerrors.Wrap(err, "record unread child message")
		}
	}

	return nil
}
