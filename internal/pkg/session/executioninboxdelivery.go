package session

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

// DeliverExecutionMessages commits the accepted IDs and transcript together.
func (s *Store) DeliverExecutionMessages(
	ctx context.Context,
	sessionID uuid.UUID,
	input DeliverExecutionMessagesInput,
) (*ExecutionInboxDelivery, error) {
	if sessionID != input.TurnLease.SessionID {
		return nil, ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"inbox delivery belongs to another session")
	}

	unlock, err := s.holdLease(input.TurnLease)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "hold inbox delivery turn")
	}
	defer unlock()

	var receipt *ExecutionInboxDelivery

	err = s.feedTransaction(ctx, func(
		ctx context.Context,
		tx *repositories.Query,
	) error {
		var deliveryErr error

		receipt, deliveryErr = s.deliverExecutionMessages(ctx, tx,
			sessionID, input)

		return deliveryErr
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "deliver inbox transaction")
	}

	ctxscope.GetLogger(ctx).Info("execution inbox delivery recorded",
		"session_id", sessionID, "activity_id", input.ModelLease.ID,
		"request_count", len(input.RequestIDs),
		"event_count", len(receipt.Events))

	return receipt, nil
}

func (s *Store) deliverExecutionMessages(
	ctx context.Context,
	tx *repositories.Query,
	sessionID uuid.UUID,
	input DeliverExecutionMessagesInput,
) (*ExecutionInboxDelivery, error) {
	activity, execution, err := s.activityForLease(ctx, tx, sessionID,
		input.ModelLease)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "find inbox delivery model activity")
	}

	if !canDeliverExecutionInbox(activity, execution, input.TurnLease) {
		return nil, ctxerrors.Wrap(commerr.ErrInvalidState,
			"model activity cannot deliver this inbox")
	}

	if err := validateInboxDeliveryIDs(activity, input.RequestIDs); err != nil {
		return nil, ctxerrors.Wrap(err, "validate saved inbox delivery IDs")
	}

	turn, err := s.findRunningTurn(ctx, tx, input.TurnLease)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read inbox delivery root turn")
	}

	var (
		messages []MessageInput
		events   []EventInput
	)

	for _, requestID := range input.RequestIDs {
		row, err := findInboxRequest(ctx, tx, sessionID, requestID)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "read delivered inbox request")
		}

		rowMessages, rowEvents, err := s.deliverInboxRow(ctx, tx, execution,
			activity, row, turn.RequestID)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "deliver accepted inbox row")
		}

		messages = append(messages, rowMessages...)
		events = append(events, rowEvents...)
	}

	generationID := input.ModelLease.GenerationID.String()
	for index := range events {
		events[index].WorkerGenerationID = generationID
	}

	if _, err := s.appendTranscript(ctx, tx, input.TurnLease,
		messages, events); err != nil {
		return nil, ctxerrors.Wrap(err, "save delivered inbox transcript")
	}

	return &ExecutionInboxDelivery{Messages: messages, Events: events}, nil
}

func validateInboxDeliveryIDs(
	activity *models.ExecutionActivity,
	requestIDs []uuid.UUID,
) error {
	var saved struct {
		Execution struct {
			Context struct {
				InboxIDs []uuid.UUID `json:"inboxIds"`
			} `json:"context"`
		} `json:"execution"`
	}
	if err := json.Unmarshal([]byte(activity.InputJSON), &saved); err != nil {
		return ctxerrors.Wrap(err, "decode saved inbox model input")
	}

	if !slices.Equal(requestIDs, saved.Execution.Context.InboxIDs) {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"delivery IDs differ from the saved model request")
	}

	seen := make(map[uuid.UUID]struct{}, len(requestIDs))
	for _, id := range requestIDs {
		if _, exists := seen[id]; exists || id == uuid.Nil {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"duplicate or missing inbox delivery identity")
		}

		seen[id] = struct{}{}
	}

	return nil
}

func canDeliverExecutionInbox(
	activity *models.ExecutionActivity,
	execution *models.Execution,
	lease Lease,
) bool {
	return activity.Kind == models.ActivityKindModel &&
		activity.State == models.ActivityStateRunning &&
		execution.Kind == models.ExecutionKindRoot &&
		execution.State != models.ExecutionStatePaused &&
		!isExecutionTerminal(execution.State) &&
		execution.RootTurnID == lease.TurnID
}
