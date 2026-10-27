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
)

func (s *Store) restoreExecutionTurnRow(
	ctx context.Context,
	tx *repositories.Query,
	execution *models.Execution,
	turn *models.Turn,
	input RestoreExecutionTurnInput,
) (Lease, error) {
	active, hasActive := s.active[execution.SessionID]
	if hasActive && active.turnID != turn.ID {
		return Lease{}, ctxerrors.Wrap(ErrSessionBusy,
			"another turn owns the session")
	}

	stored, err := findExecutionTurnLease(ctx, tx, execution)
	if err != nil && !errors.Is(err, commerr.ErrNotFound) {
		return Lease{}, err
	}

	lease, err := s.chooseExecutionTurnLease(turn, stored, input)
	if err != nil {
		return Lease{}, err
	}

	if executionTurnLeaseMatches(stored, turn, lease) &&
		s.hasLeaseLocked(lease) {
		return lease, nil
	}

	if err := s.saveExecutionTurnLease(ctx, tx, execution,
		lease, stored, input); err != nil {
		return Lease{}, err
	}

	if err := s.restoreRootTurnRow(ctx, tx, turn,
		input.WorkerGenerationID); err != nil {
		return Lease{}, err
	}

	return lease, nil
}

func (s *Store) chooseExecutionTurnLease(
	turn *models.Turn,
	stored *models.ExecutionTurnLease,
	input RestoreExecutionTurnInput,
) (Lease, error) {
	if stored != nil {
		if stored.LeaseID == uuid.Nil || stored.OwnerGenerationID == uuid.Nil {
			return Lease{}, ctxerrors.Wrap(commerr.ErrInvalidState,
				"stored execution turn holder is invalid")
		}

		if mayReuseExecutionTurnHolder(turn, stored, input) {
			return Lease{
				ID:        stored.LeaseID,
				SessionID: turn.SessionID, TurnID: turn.ID,
			}, nil
		}

		if input.PreviousLeaseID != stored.LeaseID {
			return Lease{}, ctxerrors.Wrap(commerr.ErrConflict,
				"execution turn holder changed")
		}
	}

	active, exists := s.active[turn.SessionID]
	if stored == nil && exists &&
		mayAdoptInitialTurnHolder(turn, active, input) {
		return Lease{
			ID:        active.leaseID,
			SessionID: turn.SessionID, TurnID: turn.ID,
		}, nil
	}

	return newTurnLease(turn.SessionID, turn.ID)
}

func executionTurnLeaseMatches(
	stored *models.ExecutionTurnLease,
	turn *models.Turn,
	lease Lease,
) bool {
	return stored != nil && stored.LeaseID == lease.ID &&
		turn.State == models.TurnStateRunning
}

func mayReuseExecutionTurnHolder(
	turn *models.Turn,
	stored *models.ExecutionTurnLease,
	input RestoreExecutionTurnInput,
) bool {
	workerID := uuid.Nil
	if stored.WorkerGenerationID != nil {
		workerID = *stored.WorkerGenerationID
	}

	return stored.OwnerGenerationID == input.OwnerGenerationID &&
		stored.ControlRevision == input.ControlRevision &&
		workerID == input.WorkerGenerationID && !turn.CancelRequested &&
		turn.State != models.TurnStateCancelled
}

func mayAdoptInitialTurnHolder(
	turn *models.Turn,
	active activeTurn,
	input RestoreExecutionTurnInput,
) bool {
	return active.turnID == turn.ID && !turn.CancelRequested &&
		turn.WorkerGenerationID ==
			workerLeaseReference(input.WorkerGenerationID)
}

func (s *Store) saveExecutionTurnLease(
	ctx context.Context,
	tx *repositories.Query,
	execution *models.Execution,
	lease Lease,
	stored *models.ExecutionTurnLease,
	input RestoreExecutionTurnInput,
) error {
	if stored == nil {
		stored = &models.ExecutionTurnLease{
			ExecutionID: execution.ID, SessionID: execution.SessionID,
			TurnID: execution.RootTurnID, CreatedAt: s.now(),
		}
	}

	stored.LeaseID = lease.ID
	stored.OwnerGenerationID = input.OwnerGenerationID
	stored.ControlRevision = input.ControlRevision

	stored.WorkerGenerationID = nil
	if input.WorkerGenerationID != uuid.Nil {
		stored.WorkerGenerationID = &input.WorkerGenerationID
	}

	stored.UpdatedAt = s.now()
	if err := tx.ExecutionTurnLease.WithContext(ctx).Save(stored); err != nil {
		return ctxerrors.Wrap(err, "save execution turn holder")
	}

	payload, err := json.Marshal(struct {
		LeaseID            uuid.UUID  `json:"leaseId"`
		OwnerGenerationID  uuid.UUID  `json:"ownerGenerationId"`
		WorkerGenerationID *uuid.UUID `json:"workerGenerationId,omitempty"`
	}{lease.ID, input.OwnerGenerationID, stored.WorkerGenerationID})
	if err != nil {
		return ctxerrors.Wrap(err, "encode execution turn holder event")
	}

	return s.appendExecutionEvent(ctx, tx, execution,
		ExecutionEventTypeTurnRestored, payload)
}

func (s *Store) restoreRootTurnRow(
	ctx context.Context,
	tx *repositories.Query,
	turn *models.Turn,
	generationID uuid.UUID,
) error {
	stored, err := s.findSessionWithQuery(ctx, tx, turn.SessionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read resumed root stop revision")
	}

	if turn.State != models.TurnStateRunning {
		if err := markTurnEndMessagesResumed(ctx, tx, turn); err != nil {
			return err
		}
	}

	repository := tx.Turn

	_, err = repository.WithContext(ctx).Where(
		repository.SessionID.Eq(turn.SessionID), repository.ID.Eq(turn.ID),
	).UpdateSimple(
		repository.State.Value(string(models.TurnStateRunning)),
		repository.StopRevision.Value(stored.StopRevision),
		repository.CompletedAt.Null(), repository.CancelRequested.Value(false),
		repository.FailureClassification.Value(""),
		repository.WorkerGenerationID.Value(workerLeaseReference(generationID)),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "restore original root turn")
	}

	return nil
}

func workerLeaseReference(generationID uuid.UUID) string {
	if generationID == uuid.Nil {
		return ""
	}

	return generationID.String()
}
