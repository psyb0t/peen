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

// PauseExecution commits the complete subtree's pause before work is stopped.
// Completed and failed descendants retain their terminal state.
func (s *Store) PauseExecution(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
	reason string,
) ([]uuid.UUID, error) {
	if len(reason) > s.maxStoredMessageBytes {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"execution pause reason exceeds storage limit")
	}

	payload, err := json.Marshal(struct {
		Reason string `json:"reason"`
	}{Reason: reason})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode execution pause")
	}

	return s.changeExecutionTree(ctx, sessionID, executionID,
		ExecutionEventTypePaused, reason, payload)
}

// ResumeExecution changes only paused descendants of the explicit target.
// A read, worker replacement, or unrelated task never calls this method.
func (s *Store) ResumeExecution(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
) ([]uuid.UUID, error) {
	return s.changeExecutionTree(ctx, sessionID, executionID,
		ExecutionEventTypeResumed, "", json.RawMessage(`{}`))
}

func (s *Store) changeExecutionTree(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
	kind ExecutionEventType,
	reason string,
	payload json.RawMessage,
) ([]uuid.UUID, error) {
	changed := make([]uuid.UUID, 0)

	err := s.query.Transaction(func(tx *repositories.Query) error {
		_, err := s.findExecution(ctx, tx, sessionID, executionID)
		if err != nil {
			return ctxerrors.Wrap(err, "find execution control target")
		}

		changed, err = s.changeExecutionSubtree(ctx, tx, sessionID,
			executionID, kind, reason, payload)
		if err != nil {
			return ctxerrors.Wrap(err, "change execution control subtree")
		}

		return nil
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "execution control transaction")
	}

	ctxscope.GetLogger(ctx).Info("execution control recorded",
		"session_id", sessionID, "execution_id", executionID,
		"event_type", kind, "changed_count", len(changed))

	return changed, nil
}

func (s *Store) changeExecutionSubtree(
	ctx context.Context,
	tx *repositories.Query,
	sessionID, executionID uuid.UUID,
	kind ExecutionEventType,
	reason string,
	payload json.RawMessage,
) ([]uuid.UUID, error) {
	members, err := tx.Execution.WithContext(ctx).
		Subtree(sessionID, executionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read execution control subtree")
	}

	changed := make([]uuid.UUID, 0)

	for _, execution := range members {
		if !shouldChangeExecutionState(execution.State, kind) {
			continue
		}

		if err := s.changeExecutionState(ctx, tx, execution,
			kind, reason); err != nil {
			return nil, ctxerrors.Wrap(err, "change execution control state")
		}

		if err := s.appendExecutionEvent(ctx, tx, execution,
			kind, payload); err != nil {
			return nil, ctxerrors.Wrap(err, "record execution control decision")
		}

		changed = append(changed, execution.ID)
	}

	return changed, nil
}

func shouldChangeExecutionState(
	state models.ExecutionState,
	kind ExecutionEventType,
) bool {
	if kind == ExecutionEventTypePaused {
		return state != models.ExecutionStatePaused &&
			!isExecutionTerminal(state)
	}

	return state == models.ExecutionStatePaused
}

func (s *Store) changeExecutionState(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
	kind ExecutionEventType,
	reason string,
) error {
	if execution.ControlRevision == math.MaxInt64 {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"execution control revision exhausted")
	}

	repository := query.Execution

	state := models.ExecutionStateQueued
	if execution.WorkflowExecutionID != "" {
		state = models.ExecutionStateRunning
	}

	now := s.now()

	update := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(execution.SessionID),
		repository.ID.Eq(execution.ID),
	)
	if kind == ExecutionEventTypePaused {
		_, err := update.UpdateSimple(
			repository.ControlRevision.Add(1),
			repository.State.Value(string(models.ExecutionStatePaused)),
			repository.PauseReason.Value(reason),
			repository.PausedAt.Value(now), repository.UpdatedAt.Value(now),
		)
		if err != nil {
			return ctxerrors.Wrap(err, "pause execution record")
		}

		return nil
	}

	_, err := update.UpdateSimple(
		repository.ControlRevision.Add(1),
		repository.State.Value(string(state)), repository.PausedAt.Null(),
		repository.UpdatedAt.Value(now),
	)
	if err != nil {
		return ctxerrors.Wrap(err, "resume execution record")
	}

	return nil
}
