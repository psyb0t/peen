package session

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"gorm.io/gorm"
)

func validateDeferredResumeSelection(input ResumeExecutionInput) error {
	if input.DeferredRequestID == nil && input.DeferredStopRevision == nil {
		return nil
	}

	if input.DeferredRequestID == nil || *input.DeferredRequestID == uuid.Nil ||
		input.DeferredStopRevision == nil || *input.DeferredStopRevision < 0 {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"deferred resume requires inspected request and stop revision")
	}

	return nil
}

func (s *Store) resumeDeferredInput(
	ctx context.Context,
	tx *repositories.Query,
	sessionID, executionID uuid.UUID,
	caller *models.Execution,
	input ResumeExecutionInput,
) error {
	target, err := s.findExecution(ctx, tx, sessionID, executionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read deferred resume source")
	}

	if target.Kind != models.ExecutionKindRoot ||
		target.State != models.ExecutionStateCompleted {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"deferred resume source has not completed")
	}

	if _, err := completedHookInboxWatermark(ctx, tx, target); err != nil {
		return ctxerrors.Wrap(err, "validate deferred resume completion")
	}

	repo := tx.ExecutionInboxMessage

	message, err := repo.WithContext(ctx).Where(
		repo.SessionID.Eq(sessionID), repo.ExecutionID.Eq(executionID),
		repo.RequestID.Eq(*input.DeferredRequestID),
	).First()
	if err != nil {
		return ctxerrors.Wrap(err, "read selected deferred input")
	}

	if deferredResumeSelectionChanged(message, input) {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"deferred input changed after inspection")
	}

	stored, err := s.findSessionWithQuery(ctx, tx, sessionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read deferred resume revision")
	}

	if err := reopenDeferredTurn(ctx, tx, message,
		stored.StopRevision); err != nil {
		return ctxerrors.Wrap(err, "reopen selected deferred admission")
	}

	_, err = repo.WithContext(ctx).Where(repo.ID.Eq(message.ID),
		repo.SessionID.Eq(sessionID)).UpdateSimple(
		repo.StopRevision.Value(stored.StopRevision))
	if err != nil {
		return ctxerrors.Wrap(err, "save deferred resume revision")
	}

	return s.recordDeferredResume(ctx, tx, target, caller, message, input,
		stored.StopRevision)
}

func deferredResumeSelectionChanged(
	message *models.ExecutionInboxMessage,
	input ResumeExecutionInput,
) bool {
	return message.State != models.InboxMessageStateDeferred ||
		message.ForwardedExecutionID != nil ||
		message.StopRevision != *input.DeferredStopRevision
}

func reopenDeferredTurn(
	ctx context.Context,
	tx *repositories.Query,
	message *models.ExecutionInboxMessage,
	revision int64,
) error {
	repo := tx.Turn
	id := uuid.NewSHA1(message.ID, []byte(deferredTurnIdentity))

	turn, err := repo.WithContext(ctx).Where(repo.ID.Eq(id),
		repo.SessionID.Eq(message.SessionID)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}

	if err != nil {
		return ctxerrors.Wrap(err, "read stopped deferred turn")
	}

	if turn.RequestID != message.RequestID ||
		turn.Workspace != message.WorkingDirectory ||
		turn.State == models.TurnStateCompleted {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"selected deferred turn identity or outcome differs")
	}

	_, err = repo.WithContext(ctx).Where(repo.ID.Eq(id),
		repo.SessionID.Eq(message.SessionID)).UpdateSimple(
		repo.State.Value(string(models.TurnStateInterrupted)),
		repo.CancelRequested.Value(false), repo.CompletedAt.Null(),
		repo.StopRevision.Value(revision),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "retain deferred turn continuation")
	}

	return nil
}

func (s *Store) recordDeferredResume(
	ctx context.Context,
	tx *repositories.Query,
	target, caller *models.Execution,
	message *models.ExecutionInboxMessage,
	input ResumeExecutionInput,
	revision int64,
) error {
	payload, err := json.Marshal(struct {
		SourceExecutionID uuid.UUID `json:"sourceExecutionId"`
		InboxID           uuid.UUID `json:"inboxId"`
		StopRevision      int64     `json:"stopRevision"`
		ResumeExecutionInput
	}{caller.ID, message.ID, revision, input})
	if err != nil {
		return ctxerrors.Wrap(err, "encode deferred resume provenance")
	}

	return s.appendExecutionEvent(ctx, tx, target,
		ExecutionEventTypeInboxResumed, payload)
}
