package session

import (
	"context"
	"encoding/json"
	"math"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

func (s *Store) DeliverChildExecutionMessages(
	ctx context.Context,
	sessionID uuid.UUID,
	lease ActivityLease,
	requestIDs []uuid.UUID,
) (*ExecutionInboxDelivery, error) {
	var receipt *ExecutionInboxDelivery

	err := s.query.Transaction(func(tx *repositories.Query) error {
		var deliveryErr error

		receipt, deliveryErr = s.deliverChildInboxMessages(ctx, tx, sessionID,
			lease, requestIDs)

		return deliveryErr
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "deliver child inbox transaction")
	}

	ctxscope.GetLogger(ctx).Info("child inbox delivery recorded",
		"session_id", sessionID, "activity_id", lease.ID,
		"message_count", len(receipt.Messages))

	return receipt, nil
}

func (s *Store) deliverChildInboxMessages(
	ctx context.Context,
	tx *repositories.Query,
	sessionID uuid.UUID,
	lease ActivityLease,
	requestIDs []uuid.UUID,
) (*ExecutionInboxDelivery, error) {
	activity, execution, err := s.activityForLease(ctx, tx, sessionID, lease)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read child inbox model activity")
	}

	if !canDeliverChildInbox(activity, execution) {
		return nil, ctxerrors.Wrap(commerr.ErrInvalidState,
			"child model activity cannot deliver this inbox")
	}

	if err := validateInboxDeliveryIDs(activity, requestIDs); err != nil {
		return nil, ctxerrors.Wrap(err, "validate saved child inbox IDs")
	}

	receipt := &ExecutionInboxDelivery{}

	for _, id := range requestIDs {
		row, err := findInboxRequest(ctx, tx, sessionID, id)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "read child inbox request")
		}

		messages, err := s.deliverChildInboxRow(ctx, tx, execution,
			activity, row)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "deliver child inbox row")
		}

		receipt.Messages = append(receipt.Messages, messages...)
	}

	return receipt, nil
}

func canDeliverChildInbox(
	activity *models.ExecutionActivity,
	execution *models.Execution,
) bool {
	return activity.Kind == models.ActivityKindModel &&
		activity.State == models.ActivityStateRunning &&
		execution.Kind == models.ExecutionKindChild &&
		execution.AgentRunID != nil &&
		execution.State != models.ExecutionStatePaused &&
		!isExecutionTerminal(execution.State)
}

func (s *Store) deliverChildInboxRow(
	ctx context.Context,
	tx *repositories.Query,
	execution *models.Execution,
	activity *models.ExecutionActivity,
	row *models.ExecutionInboxMessage,
) ([]MessageInput, error) {
	if row.ExecutionID != execution.ID {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"child inbox request belongs to another execution")
	}

	if row.State == models.InboxMessageStateDelivered {
		if row.DeliveredActivityID == nil ||
			*row.DeliveredActivityID != activity.ID {
			return nil, ctxerrors.Wrap(commerr.ErrConflict,
				"child request was delivered by another model activity")
		}

		return nil, nil
	}

	messages, err := inboxTranscriptMessages(row)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "decode child inbox skill parts")
	}

	inputs := make([]AgentRunMessageInput, 0, len(messages))
	for _, message := range messages {
		inputs = append(inputs, AgentRunMessageInput{
			Role: message.Role, Content: message.Content,
		})
	}

	if _, err := s.createAgentRunMessages(ctx, tx, execution.SessionID,
		*execution.AgentRunID, inputs); err != nil {
		return nil, ctxerrors.Wrap(err, "save delivered child transcript")
	}

	now := s.now()
	row.State, row.DeliveredAt = models.InboxMessageStateDelivered, &now
	row.DeliveredActivityID = &activity.ID

	if err := tx.ExecutionInboxMessage.WithContext(ctx).Save(row); err != nil {
		return nil, ctxerrors.Wrap(err, "record child delivery identity")
	}

	if err := s.recordChildInboxDelivery(ctx, tx, execution,
		activity, row); err != nil {
		return nil, ctxerrors.Wrap(err, "record child delivery history")
	}

	return messages, nil
}

func (s *Store) recordChildInboxDelivery(
	ctx context.Context,
	tx *repositories.Query,
	execution *models.Execution,
	activity *models.ExecutionActivity,
	row *models.ExecutionInboxMessage,
) error {
	if execution.EventCount == math.MaxInt64 {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"child delivery event sequence is exhausted")
	}

	payload := struct {
		InboxID    uuid.UUID `json:"inboxId"`
		RequestID  uuid.UUID `json:"requestId"`
		ActivityID uuid.UUID `json:"activityId"`
		AgentRunID uuid.UUID `json:"agentRunId"`
	}{row.ID, row.RequestID, activity.ID, *execution.AgentRunID}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return ctxerrors.Wrap(err, "encode child delivery history")
	}

	return s.appendExecutionEvent(ctx, tx, execution,
		ExecutionEventTypeInboxDelivered, encoded)
}
