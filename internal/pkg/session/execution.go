package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

// insertExecution writes created with its context compacted, then restores
// the context on created, which callers keep using.
func insertExecution(
	ctx context.Context,
	tx *repositories.Query,
	created *models.Execution,
) error {
	contextJSON := created.ContextJSON

	compacted, err := compactColumn(ctx, tx, contextJSON)
	if err != nil {
		return ctxerrors.Wrap(err, "compact execution context")
	}

	created.ContextJSON = compacted

	err = tx.Execution.WithContext(ctx).Create(created)
	created.ContextJSON = contextJSON

	if err != nil {
		return ctxerrors.Wrap(err, "create execution record")
	}

	return nil
}

// CreateExecution records an immutable identity before scheduling a workflow.
// Duplicate creation returns the saved execution and never wakes paused work.
func (s *Store) CreateExecution(
	ctx context.Context,
	sessionID uuid.UUID,
	input CreateExecutionInput,
) (*models.Execution, error) {
	if err := s.validateExecutionInput(input); err != nil {
		return nil, err
	}

	var result *models.Execution

	err := s.query.Transaction(func(tx *repositories.Query) error {
		existing, err := s.findExecution(ctx, tx, sessionID, input.ID)
		if err == nil {
			if !sameExecutionIdentity(existing, input) {
				return ctxerrors.Wrap(
					commerr.ErrConflict, "execution identity changed",
				)
			}

			result = existing

			return s.bindDeferredExecution(ctx, tx, existing,
				input.DeferredInboxID)
		}

		if !errors.Is(err, commerr.ErrNotFound) {
			return ctxerrors.Wrap(err, "find execution before creation")
		}

		created, err := s.executionRecord(ctx, tx, sessionID, input)
		if err != nil {
			return ctxerrors.Wrap(err, "prepare execution record")
		}

		if err := insertExecution(ctx, tx, created); err != nil {
			return err
		}

		if err := s.bindDeferredExecution(ctx, tx, created,
			input.DeferredInboxID); err != nil {
			return ctxerrors.Wrap(err, "bind deferred root creation")
		}

		if err := s.recordExecutionCreation(ctx, tx, created); err != nil {
			return ctxerrors.Wrap(err, "record execution creation")
		}

		result = created

		return nil
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create execution transaction")
	}

	return result, nil
}

func (s *Store) recordExecutionCreation(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
) error {
	if err := s.appendExecutionEvent(ctx, query, execution,
		ExecutionEventTypeCreated, json.RawMessage(`{}`)); err != nil {
		return ctxerrors.Wrap(err, "record execution creation event")
	}

	return s.recordInitialExecutionPause(ctx, query, execution)
}

func (s *Store) executionRecord(
	ctx context.Context,
	tx *repositories.Query,
	sessionID uuid.UUID,
	input CreateExecutionInput,
) (*models.Execution, error) {
	turn := tx.Turn

	rootTurn, err := turn.WithContext(ctx).Where(
		turn.SessionID.Eq(sessionID), turn.ID.Eq(input.RootTurnID),
	).First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "find execution root turn")
	}

	now := s.now()

	created := &models.Execution{
		ID: input.ID, SessionID: sessionID, RootTurnID: input.RootTurnID,
		RootExecutionID: input.ID, ParentExecutionID: input.ParentExecutionID,
		AgentRunID: input.AgentRunID, Kind: models.ExecutionKindRoot,
		State:               models.ExecutionStateQueued,
		WorkflowVersion:     input.WorkflowVersion,
		WorkflowExecutionID: input.WorkflowExecutionID,
		Task:                input.Task,
		WorkingDirectory:    input.WorkingDirectory,
		ContextJSON:         string(input.Context),
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if input.ParentExecutionID == nil {
		return s.inheritSessionStop(ctx, tx, created, rootTurn)
	}

	return s.inheritExecutionParent(ctx, tx, created)
}

func (s *Store) inheritExecutionParent(
	ctx context.Context,
	tx *repositories.Query,
	created *models.Execution,
) (*models.Execution, error) {
	parent, err := s.findExecution(ctx, tx,
		created.SessionID, *created.ParentExecutionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "find parent execution")
	}

	if parent.RootTurnID != created.RootTurnID ||
		isExecutionTerminal(parent.State) {
		return nil, ctxerrors.Wrap(
			commerr.ErrConflict, "parent execution state",
		)
	}

	child, err := s.findAgentRunWithQuery(ctx, tx,
		created.SessionID, *created.AgentRunID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "find execution child run")
	}

	if child.CancelRequested ||
		child.ParentTurnID != created.RootTurnID ||
		!sameOptionalExecutionID(child.ParentAgentRunID, parent.AgentRunID) {
		return nil, ctxerrors.Wrap(commerr.ErrConflict, "child run lineage")
	}

	created.Kind = models.ExecutionKindChild

	created.RootExecutionID = parent.RootExecutionID
	if parent.State == models.ExecutionStatePaused {
		created.State = parent.State
		created.PauseReason = parent.PauseReason
		created.PausedAt = &created.CreatedAt
	}

	return created, nil
}

func sameExecutionIdentity(
	existing *models.Execution,
	input CreateExecutionInput,
) bool {
	hasSameParent := sameOptionalExecutionID(
		existing.ParentExecutionID, input.ParentExecutionID,
	)

	return existing.RootTurnID == input.RootTurnID &&
		existing.WorkflowVersion == input.WorkflowVersion &&
		existing.WorkflowExecutionID == input.WorkflowExecutionID &&
		existing.Task == input.Task &&
		hasSameParent &&
		sameOptionalExecutionID(existing.AgentRunID, input.AgentRunID)
}

func sameOptionalExecutionID(left, right *uuid.UUID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}

	return *left == *right
}

func (s *Store) GetExecution(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
) (*models.Execution, error) {
	return s.findExecution(ctx, s.query, sessionID, executionID)
}

func (s *Store) findExecution(
	ctx context.Context,
	query *repositories.Query,
	sessionID, executionID uuid.UUID,
) (*models.Execution, error) {
	repository := query.Execution

	result, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(sessionID), repository.ID.Eq(executionID),
	).First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "find session execution")
	}

	return result, nil
}

func isExecutionTerminal(state models.ExecutionState) bool {
	switch state {
	case models.ExecutionStateCompleted, models.ExecutionStateFailed,
		models.ExecutionStateCancelled:
		return true
	case models.ExecutionStateQueued, models.ExecutionStateRunning,
		models.ExecutionStatePaused, models.ExecutionStateReconciling:
		return false
	}

	return false
}

func (s *Store) appendExecutionEvent(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
	kind ExecutionEventType,
	payload json.RawMessage,
) error {
	if execution.EventCount == math.MaxInt64 {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"execution event sequence exhausted")
	}

	sequence := execution.EventCount + 1

	event := &models.ExecutionEvent{
		ID: s.newID(), SessionID: execution.SessionID,
		ExecutionID: execution.ID,
		Sequence:    sequence, EventType: string(kind),
		PayloadJSON: string(payload),
		CreatedAt:   s.now(),
	}
	if err := query.ExecutionEvent.WithContext(ctx).Create(event); err != nil {
		return ctxerrors.Wrap(err, "append execution event")
	}

	repository := query.Execution

	updatedAt := s.now()
	if _, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(execution.SessionID),
		repository.ID.Eq(execution.ID),
	).UpdateColumnSimple(
		repository.EventCount.Value(sequence),
		repository.UpdatedAt.Value(updatedAt),
	); err != nil {
		return ctxerrors.Wrap(err, "advance execution event sequence")
	}

	execution.EventCount = sequence
	execution.UpdatedAt = updatedAt

	return nil
}

func (s *Store) validateExecutionInput(input CreateExecutionInput) error {
	if input.ID == uuid.Nil || input.RootTurnID == uuid.Nil ||
		input.WorkflowVersion == "" || input.WorkingDirectory == "" ||
		!isExecutionContext(input.Context) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"execution identity and context")
	}

	if len(input.Context) > s.maxExecutionStateBytes ||
		len(input.Task) > s.maxStoredMessageBytes {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"execution context or task exceeds storage limit")
	}

	return validateExecutionLineageInput(input)
}

func isExecutionContext(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)

	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed)
}

func validateExecutionLineageInput(input CreateExecutionInput) error {
	if input.ParentExecutionID == nil {
		if input.AgentRunID != nil {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"root execution cannot name a child run")
		}

		return nil
	}

	if input.AgentRunID == nil || *input.AgentRunID == uuid.Nil ||
		*input.ParentExecutionID == uuid.Nil ||
		*input.ParentExecutionID == input.ID {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"child execution identity")
	}

	return nil
}
