package session

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"gorm.io/gorm"
)

const deferredTurnIdentity = "deferredRootTurnV1"

type DeferredTurnAdmission struct {
	Lease   Lease                   `json:"lease"`
	Events  []EventInput            `json:"events"`
	Created bool                    `json:"created"`
	Notices []*models.SessionNotice `json:"notices,omitempty"`
}

// AcquireDeferredExecutionTurn admits accepted input once across restarts.
// Retries retain the turn and transcript; cancelled work remains stopped.
func (s *Store) AcquireDeferredExecutionTurn(
	ctx context.Context,
	sessionID, inboxID uuid.UUID,
	input StartTurnInput,
) (*DeferredTurnAdmission, error) {
	if sessionID == uuid.Nil || inboxID == uuid.Nil {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"deferred turn identity")
	}

	turnID := uuid.NewSHA1(inboxID, []byte(deferredTurnIdentity))

	s.activeMu.Lock()
	defer s.activeMu.Unlock()

	active, exists := s.active[sessionID]
	if exists && active.turnID != turnID {
		return nil, ctxerrors.Wrap(ErrSessionBusy,
			"another turn owns deferred admission")
	}

	lease, err := newTurnLease(sessionID, turnID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create deferred turn holder")
	}

	if exists {
		lease.ID = active.leaseID
	}

	result := &DeferredTurnAdmission{Lease: lease}

	if err := s.feedTransaction(ctx, func(
		ctx context.Context,
		tx *repositories.Query,
	) error {
		return s.admitDeferredTurn(ctx, tx, inboxID, input, result)
	}); err != nil {
		return nil, ctxerrors.Wrap(err, "admit deferred turn transaction")
	}

	if !exists {
		s.active[sessionID] = activeTurn{turnID: turnID, leaseID: lease.ID}
	}

	ctxscope.GetLogger(ctx).Info("deferred turn admitted",
		"session_id", sessionID, "turn_id", turnID,
		"inbox_id", inboxID, "created", result.Created,
		"opening_notice_count", len(result.Notices))

	return result, nil
}

func (s *Store) admitDeferredTurn(
	ctx context.Context,
	tx *repositories.Query,
	inboxID uuid.UUID,
	input StartTurnInput,
	result *DeferredTurnAdmission,
) error {
	if err := s.validateDeferredTurnInput(ctx, tx, result.Lease.SessionID,
		inboxID, input); err != nil {
		return ctxerrors.Wrap(err, "validate deferred admission")
	}

	repository := tx.Turn

	turn, err := repository.WithContext(ctx).Where(
		repository.ID.Eq(result.Lease.TurnID),
		repository.SessionID.Eq(result.Lease.SessionID),
	).First()
	if err == nil {
		if err := s.restoreDeferredTurn(ctx, tx, turn, input); err != nil {
			return ctxerrors.Wrap(err, "restore retained deferred turn")
		}

		result.Notices, err = readTurnOpeningNotices(ctx, tx, result.Lease)
		if err != nil {
			return ctxerrors.Wrap(err, "restore deferred opening notices")
		}

		return s.validateWakeAdmission(ctx, tx, result.Lease.SessionID,
			input.WakeNoticeID, result.Notices)
	}

	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return ctxerrors.Wrap(err, "read deferred turn admission")
	}

	return s.createDeferredTurn(ctx, tx, input, result)
}

func (s *Store) createDeferredTurn(
	ctx context.Context,
	tx *repositories.Query,
	input StartTurnInput,
	result *DeferredTurnAdmission,
) error {
	repository := tx.Turn

	revision, err := s.admissionStopRevision(ctx, tx,
		result.Lease.SessionID, input.ExpectedStopRevision)
	if err != nil {
		return ctxerrors.Wrap(err, "read deferred turn stop revision")
	}

	turn := &models.Turn{
		ID: result.Lease.TurnID, SessionID: result.Lease.SessionID,
		RequestID: input.RequestID, Workspace: input.Workspace,
		State: models.TurnStateRunning, StartedAt: s.now(),
		WorkerGenerationID: input.WorkerGenerationID,
		StopRevision:       revision,
	}
	if err := repository.WithContext(ctx).Create(turn); err != nil {
		return ctxerrors.Wrap(err, "create deferred turn")
	}

	if _, err := s.appendTranscript(ctx, tx, result.Lease,
		input.Messages, input.Events); err != nil {
		return ctxerrors.Wrap(err, "record deferred admission transcript")
	}

	notices, err := s.deliverOpeningNotices(ctx, tx, result.Lease, input)
	if err != nil {
		return ctxerrors.Wrap(err, "admit deferred opening notices")
	}

	result.Notices = notices
	result.Events = input.Events
	result.Created = true

	return nil
}

func (s *Store) validateDeferredTurnInput(
	ctx context.Context,
	tx *repositories.Query,
	sessionID, inboxID uuid.UUID,
	input StartTurnInput,
) error {
	repository := tx.ExecutionInboxMessage

	message, err := repository.WithContext(ctx).Where(
		repository.ID.Eq(inboxID), repository.SessionID.Eq(sessionID),
	).First()
	if err != nil {
		return ctxerrors.Wrap(err, "read deferred admission input")
	}

	if err := s.validateDeferredStopRevision(ctx, tx, sessionID,
		input.ExpectedStopRevision, message); err != nil {
		return ctxerrors.Wrap(err, "validate deferred stop revision")
	}

	if message.State != models.InboxMessageStateDeferred ||
		message.RequestID != input.RequestID ||
		message.WorkingDirectory != input.Workspace {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"deferred admission identity differs")
	}

	source, err := s.findExecution(ctx, tx, sessionID, message.ExecutionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read deferred admission source")
	}

	if source.Kind != models.ExecutionKindRoot ||
		source.State != models.ExecutionStateCompleted {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"deferred admission source is unfinished")
	}

	if _, err := completedHookInboxWatermark(ctx, tx, source); err != nil {
		return ctxerrors.Wrap(err, "validate deferred admission completion")
	}

	return validateDeferredTurnWorker(ctx, tx, sessionID,
		input.WorkerGenerationID)
}

func validateDeferredTurnWorker(
	ctx context.Context,
	tx *repositories.Query,
	sessionID uuid.UUID,
	workerID string,
) error {
	if workerID == "" {
		return nil
	}

	generationID, err := uuid.Parse(workerID)
	if err != nil || generationID == uuid.Nil {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"deferred admission worker identity")
	}

	return validateExecutionTurnWorker(ctx, tx, sessionID, generationID)
}

func (s *Store) restoreDeferredTurn(
	ctx context.Context,
	tx *repositories.Query,
	turn *models.Turn,
	input StartTurnInput,
) error {
	if turn.RequestID != input.RequestID || turn.Workspace != input.Workspace {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"stored deferred turn identity differs")
	}

	if turn.CancelRequested || (turn.State != models.TurnStateRunning &&
		turn.State != models.TurnStateInterrupted) {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"deferred turn remains stopped")
	}

	if active, exists := s.active[turn.SessionID]; exists &&
		(active.turnID != turn.ID ||
			turn.WorkerGenerationID != input.WorkerGenerationID) {
		return ctxerrors.Wrap(ErrSessionBusy,
			"deferred turn still has another holder")
	}

	repository := tx.Turn

	_, err := repository.WithContext(ctx).Where(
		repository.ID.Eq(turn.ID), repository.SessionID.Eq(turn.SessionID),
	).UpdateSimple(
		repository.State.Value(string(models.TurnStateRunning)),
		repository.CompletedAt.Null(),
		repository.WorkerGenerationID.Value(input.WorkerGenerationID),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "restore deferred turn admission")
	}

	return nil
}
