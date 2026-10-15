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

func (s *Store) validateChildProjection(input CompleteActivityInput) error {
	projection := input.ChildProjection
	if projection == nil {
		return nil
	}

	if !validChildProjectionPayload(input) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"child projection cannot mix completion payloads")
	}

	encoded, err := json.Marshal(projection)
	if err != nil {
		return ctxerrors.Wrap(err, "encode child projection")
	}

	if len(encoded) > s.maxStoredMessageBytes {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"child projection exceeds storage limit")
	}

	if err := validateProjectedChildMessages(projection.Messages); err != nil {
		return ctxerrors.Wrap(err, "validate child projection messages")
	}

	for _, event := range projection.Events {
		if event.ID == uuid.Nil {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"child projection event identity")
		}
	}

	return validateChildQuestionProjection(projection)
}

func validChildProjectionPayload(input CompleteActivityInput) bool {
	return input.RootProjection == nil && input.Finish == nil &&
		input.Compaction == nil && input.ChildProjection.ControlRevision >= 0
}

func validateProjectedChildMessages(messages []AgentRunMessageInput) error {
	for _, message := range messages {
		if err := validateAgentRunMessageInput(message); err != nil {
			return ctxerrors.Wrap(err, "validate projected child message")
		}

		if message.ToolCallsJSON != "" &&
			!json.Valid([]byte(message.ToolCallsJSON)) {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"projected child tool calls JSON")
		}
	}

	return nil
}

func (s *Store) saveChildActivityProjection(
	ctx context.Context,
	query *repositories.Query,
	activity *models.ExecutionActivity,
	execution *models.Execution,
	input *ExecutionChildProjection,
) error {
	if input == nil {
		return nil
	}

	run, err := s.childProjectionOwner(ctx, query, activity, execution, input)
	if err != nil {
		return ctxerrors.Wrap(err, "read child projection owner")
	}

	if len(input.Messages) != 0 {
		if _, err := s.createAgentRunMessages(ctx, query, run.SessionID,
			run.ID, input.Messages); err != nil {
			return ctxerrors.Wrap(err, "append child activity messages")
		}
	}

	if err := s.saveChildProjectionEvents(ctx, query, run,
		input.Events); err != nil {
		return ctxerrors.Wrap(err, "append child activity events")
	}

	if err := s.saveChildQuestion(ctx, query, execution, run,
		input.Question); err != nil {
		return ctxerrors.Wrap(err, "save child question delivery")
	}

	return saveProjectionIdentity(ctx, query, activity, input)
}

func (s *Store) childProjectionOwner(
	ctx context.Context,
	query *repositories.Query,
	activity *models.ExecutionActivity,
	execution *models.Execution,
	input *ExecutionChildProjection,
) (*models.AgentRun, error) {
	if execution.Kind != models.ExecutionKindChild ||
		execution.AgentRunID == nil ||
		execution.State != models.ExecutionStateRunning ||
		execution.ControlRevision != input.ControlRevision {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"child projection execution changed")
	}

	run, err := s.findAgentRunWithQuery(ctx, query, execution.SessionID,
		*execution.AgentRunID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read projected child run")
	}

	if run.State != models.AgentRunStateRunning || run.CancelRequested ||
		run.WorkerGenerationID != activity.OwnerGenerationID ||
		run.ParentTurnID != execution.RootTurnID {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"child projection actor ownership changed")
	}

	return run, nil
}

func (s *Store) saveChildProjectionEvents(
	ctx context.Context,
	query *repositories.Query,
	run *models.AgentRun,
	inputs []AgentRunEventInput,
) error {
	if len(inputs) == 0 {
		return nil
	}

	count := int64(len(inputs))
	if count > math.MaxInt64-run.EventCount {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"child projection event counter exhausted")
	}

	sequence, err := nextAgentRunEventSequence(ctx, query, run.ID)
	if err != nil {
		return ctxerrors.Wrap(err, "allocate projected child event sequence")
	}

	if sequence < 1 || sequence > math.MaxInt64-count+1 {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"child projection event sequence exhausted")
	}

	for index, input := range inputs {
		normalized, err := s.normalizeAgentRunEventInput(input)
		if err != nil {
			return ctxerrors.Wrap(err, "validate projected child event")
		}

		event := &models.AgentRunEvent{
			ID: normalized.ID, SessionID: run.SessionID, AgentRunID: run.ID,
			Sequence: sequence + int64(index), EventType: normalized.EventType,
			PayloadJSON: normalized.PayloadJSON,
			CreatedAt:   normalized.CreatedAt,
		}
		if err := query.AgentRunEvent.WithContext(ctx).
			Create(event); err != nil {
			return ctxerrors.Wrap(err, "create projected child event")
		}
	}

	repository := query.AgentRun

	_, err = repository.WithContext(ctx).Where(
		repository.SessionID.Eq(run.SessionID), repository.ID.Eq(run.ID),
	).UpdateSimple(repository.EventCount.Add(count))
	if err != nil {
		return ctxerrors.Wrap(err, "count projected child events")
	}

	return nil
}
