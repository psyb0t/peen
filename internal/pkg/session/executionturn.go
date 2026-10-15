package session

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

type executionTurnRestoration struct {
	lease          Lease
	cancelPrevious context.CancelFunc
}

// RestoreExecutionTurn retains the original turn and never resumes a pause.
func (s *Store) RestoreExecutionTurn(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
	input RestoreExecutionTurnInput,
) (Lease, error) {
	if input.ControlRevision < 0 || input.OwnerGenerationID == uuid.Nil ||
		sessionID == uuid.Nil ||
		executionID == uuid.Nil || (input.WorkerGenerationID != uuid.Nil &&
		input.WorkerGenerationID != input.OwnerGenerationID) {
		return Lease{}, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"execution turn holder identity")
	}

	result, err := s.restoreExecutionTurn(ctx, sessionID, executionID, input)
	if err != nil {
		return Lease{}, err
	}

	if result.cancelPrevious != nil {
		result.cancelPrevious()
	}

	ctxscope.GetLogger(ctx).Info("execution turn lease restored",
		"execution_id", executionID, "turn_id", result.lease.TurnID,
		"owner_generation_id", input.OwnerGenerationID)

	return result.lease, nil
}

func (s *Store) restoreExecutionTurn(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
	input RestoreExecutionTurnInput,
) (executionTurnRestoration, error) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()

	var result executionTurnRestoration

	err := s.query.Transaction(func(tx *repositories.Query) error {
		execution, err := s.findExecution(ctx, tx, sessionID, executionID)
		if err != nil {
			return err
		}

		if err := validateExecutionTurnRestore(execution); err != nil {
			return err
		}

		if execution.ControlRevision != input.ControlRevision {
			return ctxerrors.Wrap(commerr.ErrConflict,
				"execution turn control revision changed")
		}

		if err := validateExecutionTurnWorker(ctx, tx,
			sessionID, input.WorkerGenerationID); err != nil {
			return err
		}

		turn, err := findExecutionRootTurn(ctx, tx, execution)
		if err != nil {
			return err
		}

		result.lease, err = s.restoreExecutionTurnRow(ctx, tx,
			execution, turn, input)

		return err
	})
	if err != nil {
		return result, ctxerrors.Wrap(err, "restore execution turn transaction")
	}

	active := s.active[sessionID]
	if s.hasLeaseLocked(result.lease) {
		return result, nil
	}

	result.cancelPrevious = active.cancel
	s.active[sessionID] = activeTurn{
		turnID: result.lease.TurnID, leaseID: result.lease.ID,
	}

	return result, nil
}

func validateExecutionTurnRestore(execution *models.Execution) error {
	if execution.Kind != models.ExecutionKindRoot {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"execution turn holder requires a root")
	}

	if execution.State == models.ExecutionStatePaused ||
		isExecutionTerminal(execution.State) {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"execution cannot restore its turn in this state")
	}

	return nil
}

func validateExecutionTurnWorker(
	ctx context.Context,
	tx *repositories.Query,
	sessionID, generationID uuid.UUID,
) error {
	if generationID == uuid.Nil {
		return nil
	}

	repository := tx.WorkerGeneration

	current, err := repository.WithContext(ctx).
		Where(repository.SessionID.Eq(sessionID)).
		Order(repository.CreatedAt.Desc(), repository.ID.Desc()).First()
	if err != nil {
		return ctxerrors.Wrap(err, "find root holder worker")
	}

	if current.ID != generationID || (current.State != workerStateStarting &&
		current.State != workerStateReady) {
		return ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"root holder worker is not current and active")
	}

	return nil
}

func findExecutionRootTurn(
	ctx context.Context,
	tx *repositories.Query,
	execution *models.Execution,
) (*models.Turn, error) {
	repository := tx.Turn

	turn, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(execution.SessionID),
		repository.ID.Eq(execution.RootTurnID),
	).First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "find original execution turn")
	}

	if turn.State == models.TurnStateCompleted ||
		turn.State == models.TurnStateFailed {
		return nil, ctxerrors.Wrap(commerr.ErrInvalidState,
			"original execution turn is terminal")
	}

	return turn, nil
}

func (s *Store) GetExecutionTurnLease(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
) (*models.ExecutionTurnLease, error) {
	repository := s.query.ExecutionTurnLease

	stored, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(sessionID),
		repository.ExecutionID.Eq(executionID),
	).First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read execution turn lease")
	}

	return stored, nil
}

func findExecutionTurnLease(
	ctx context.Context,
	tx *repositories.Query,
	execution *models.Execution,
) (*models.ExecutionTurnLease, error) {
	repository := tx.ExecutionTurnLease

	stored, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(execution.SessionID),
		repository.ExecutionID.Eq(execution.ID),
	).First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "find execution turn lease")
	}

	return stored, nil
}
