package session

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

func (s *Store) pauseSessionExecutions(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
) (bool, error) {
	repository := query.Execution

	executions, err := repository.WithContext(ctx).
		Where(repository.SessionID.Eq(sessionID)).Find()
	if err != nil {
		return false, ctxerrors.Wrap(err, "read stopped executions")
	}

	changed := false

	for _, execution := range executions {
		if !shouldChangeExecutionState(execution.State,
			ExecutionEventTypePaused) {
			continue
		}

		if err := s.changeExecutionState(ctx, query, execution,
			ExecutionEventTypePaused, ExecutionUserStopReason); err != nil {
			return false, ctxerrors.Wrap(err, "persist session execution stop")
		}

		execution.PauseReason = ExecutionUserStopReason
		if err := s.appendExecutionPause(ctx, query, execution); err != nil {
			return false, ctxerrors.Wrap(err, "record session execution stop")
		}

		changed = true
	}

	return changed, nil
}

func (s *Store) inheritSessionStop(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
	turn *models.Turn,
) (*models.Execution, error) {
	stored, err := s.findSessionWithQuery(ctx, query, execution.SessionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read execution stop revision")
	}

	if turn.StopRevision != stored.StopRevision || turn.CancelRequested ||
		turn.State == models.TurnStateCancelled {
		execution.State = models.ExecutionStatePaused
		execution.PauseReason = ExecutionUserStopReason
		execution.PausedAt = &execution.CreatedAt
	}

	return execution, nil
}

func (s *Store) recordInitialExecutionPause(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
) error {
	if execution.State != models.ExecutionStatePaused {
		return nil
	}

	return s.appendExecutionPause(ctx, query, execution)
}

func (s *Store) appendExecutionPause(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
) error {
	payload, err := json.Marshal(struct {
		Reason string `json:"reason"`
	}{Reason: execution.PauseReason})
	if err != nil {
		return ctxerrors.Wrap(err, "encode session execution stop")
	}

	return s.appendExecutionEvent(ctx, query, execution,
		ExecutionEventTypePaused, payload)
}
