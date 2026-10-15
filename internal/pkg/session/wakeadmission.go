package session

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/events"
)

func (s *Store) validateWakeAdmission(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	wakeID *uuid.UUID,
	notices []*models.SessionNotice,
) error {
	if wakeID == nil {
		return nil
	}

	if *wakeID == uuid.Nil {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"wake notice identity")
	}

	for _, notice := range notices {
		if notice.ID != *wakeID {
			continue
		}

		if notice.SessionID != sessionID {
			return ctxerrors.Wrap(commerr.ErrPermissionDenied,
				"wake notice owner")
		}

		return s.validateWakeSource(ctx, query, sessionID, notice)
	}

	return ctxerrors.Wrap(commerr.ErrConflict,
		"wake notice is absent from admitted opening batch")
}

func (s *Store) validateAgentWakeLineage(
	ctx context.Context,
	query *repositories.Query,
	sessionID, runID uuid.UUID,
) error {
	seen := make(map[uuid.UUID]struct{})
	for {
		if _, duplicate := seen[runID]; duplicate {
			return ctxerrors.Wrap(commerr.ErrConflict,
				"agent wake lineage repeats")
		}

		seen[runID] = struct{}{}

		run, err := s.findAgentRunWithQuery(ctx, query, sessionID, runID)
		if err != nil {
			return ctxerrors.Wrap(err, "read owned wake source run")
		}

		if run.CancelRequested || run.State == models.AgentRunStateCancelled {
			return ctxerrors.Wrap(commerr.ErrInvalidState,
				"agent wake source remains stopped")
		}

		if err := s.validateWakeTurn(ctx, query, sessionID,
			run.ParentTurnID); err != nil {
			return ctxerrors.Wrap(err, "validate agent wake source turn")
		}

		if err := s.validateAgentWakeExecutions(ctx, query, run); err != nil {
			return ctxerrors.Wrap(err, "validate agent wake execution lineage")
		}

		if run.ParentAgentRunID == nil {
			return nil
		}

		runID = *run.ParentAgentRunID
	}
}

func (s *Store) validateWakeTurn(
	ctx context.Context,
	query *repositories.Query,
	sessionID, turnID uuid.UUID,
) error {
	repository := query.Turn

	turn, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(sessionID), repository.ID.Eq(turnID),
	).First()
	if err != nil {
		return ctxerrors.Wrap(err, "read owned wake source turn")
	}

	if turn.CancelRequested || turn.State == models.TurnStateCancelled {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"wake source turn remains stopped")
	}

	executions := query.Execution

	roots, err := executions.WithContext(ctx).Where(
		executions.SessionID.Eq(sessionID), executions.RootTurnID.Eq(turnID),
		executions.Kind.Eq(string(models.ExecutionKindRoot)),
	).Find()
	if err != nil {
		return ctxerrors.Wrap(err, "read wake source root executions")
	}

	for _, root := range roots {
		if err := s.validateWakeExecutionLineage(ctx, query, root); err != nil {
			return ctxerrors.Wrap(err, "validate wake source root state")
		}
	}

	return nil
}

func (s *Store) validateAgentWakeExecutions(
	ctx context.Context,
	query *repositories.Query,
	run *models.AgentRun,
) error {
	repository := query.Execution

	executions, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(run.SessionID),
		repository.AgentRunID.Eq(run.ID),
	).Find()
	if err != nil {
		return ctxerrors.Wrap(err, "read wake source execution")
	}

	for _, execution := range executions {
		if err := s.validateWakeExecutionLineage(ctx, query,
			execution); err != nil {
			return ctxerrors.Wrap(err, "validate wake execution ancestry")
		}
	}

	return nil
}

func (s *Store) validateWakeExecutionLineage(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
) error {
	seen := make(map[uuid.UUID]struct{})
	for {
		if _, duplicate := seen[execution.ID]; duplicate {
			return ctxerrors.Wrap(commerr.ErrConflict,
				"wake execution lineage repeats")
		}

		seen[execution.ID] = struct{}{}
		if execution.State == models.ExecutionStatePaused {
			return ctxerrors.Wrap(commerr.ErrInvalidState,
				"wake source execution remains paused")
		}

		if execution.ParentExecutionID == nil {
			return nil
		}

		parent, err := s.findExecution(ctx, query, execution.SessionID,
			*execution.ParentExecutionID)
		if err != nil {
			return ctxerrors.Wrap(err, "read owned wake execution ancestor")
		}

		execution = parent
	}
}

func (s *Store) validateWakeSource(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	notice *models.SessionNotice,
) error {
	switch notice.Source {
	case events.JobSource:
		return s.validateJobWake(ctx, query, sessionID, notice)
	case events.AgentSource:
		var data events.AgentNoticeData
		if err := json.Unmarshal([]byte(notice.DataJSON), &data); err != nil {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"decode agent wake identity")
		}

		if data.RunID == uuid.Nil {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"agent wake run identity")
		}

		return s.validateAgentWakeLineage(ctx, query, sessionID, data.RunID)
	default:
		return nil
	}
}
