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

func (s *Store) bindDeferredExecution(
	ctx context.Context,
	query *repositories.Query,
	target *models.Execution,
	inboxID *uuid.UUID,
) error {
	if inboxID == nil {
		return nil
	}

	repository := query.ExecutionInboxMessage

	message, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(target.SessionID), repository.ID.Eq(*inboxID),
	).First()
	if err != nil {
		return ctxerrors.Wrap(err, "read deferred root input")
	}

	if message.State == models.InboxMessageStateForwarded {
		if message.ForwardedExecutionID != nil &&
			*message.ForwardedExecutionID == target.ID {
			return nil
		}

		return ctxerrors.Wrap(commerr.ErrConflict,
			"deferred message already belongs to another root")
	}

	if err := validateDeferredTarget(ctx, query, target, message); err != nil {
		return ctxerrors.Wrap(err, "validate deferred root identity")
	}

	if message.State != models.InboxMessageStateDeferred ||
		target.State != models.ExecutionStateQueued {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"deferred handoff requires an unclaimed input and new root")
	}

	return s.forwardExecutionMessage(ctx, query, target, message)
}

func validateDeferredTarget(
	ctx context.Context,
	query *repositories.Query,
	target *models.Execution,
	message *models.ExecutionInboxMessage,
) error {
	if target.Kind != models.ExecutionKindRoot ||
		target.ID == message.ExecutionID || target.Task != message.Message ||
		target.WorkingDirectory != message.WorkingDirectory {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"deferred root text or directory differs")
	}

	repository := query.Turn

	turn, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(target.SessionID),
		repository.ID.Eq(target.RootTurnID),
	).First()
	if err != nil {
		return ctxerrors.Wrap(err, "read deferred target turn")
	}

	if turn.RequestID != message.RequestID {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"deferred target request differs")
	}

	return nil
}

func (s *Store) forwardExecutionMessage(
	ctx context.Context,
	query *repositories.Query,
	target *models.Execution,
	message *models.ExecutionInboxMessage,
) error {
	source, err := s.findExecution(ctx, query, target.SessionID,
		message.ExecutionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read deferred source execution")
	}

	if source.State != models.ExecutionStateCompleted ||
		source.EventCount == math.MaxInt64 {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"deferred source completion is unavailable")
	}

	if _, err := completedHookInboxWatermark(ctx, query, source); err != nil {
		return ctxerrors.Wrap(err, "validate deferred source completion")
	}

	repository := query.ExecutionInboxMessage

	_, err = repository.WithContext(ctx).Where(
		repository.SessionID.Eq(target.SessionID), repository.ID.Eq(message.ID),
	).UpdateSimple(
		repository.State.Value(string(models.InboxMessageStateForwarded)),
		repository.ForwardedExecutionID.Value(target.ID),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "save deferred execution handoff")
	}

	payload, err := json.Marshal(struct {
		InboxID           uuid.UUID `json:"inboxId"`
		TargetExecutionID uuid.UUID `json:"targetExecutionId"`
	}{message.ID, target.ID})
	if err != nil {
		return ctxerrors.Wrap(err, "encode deferred execution handoff")
	}

	return s.appendExecutionEvent(ctx, query, source,
		ExecutionEventTypeInboxForwarded, payload)
}
