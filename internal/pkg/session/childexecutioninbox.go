package session

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

func (s *Store) AcceptChildExecutionMessage(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
	input AcceptExecutionMessageInput,
) (*ExecutionInboxAcceptance, error) {
	parts, err := s.validateExecutionInboxInput(input)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "validate accepted child message")
	}

	var result *ExecutionInboxAcceptance

	err = s.query.Transaction(func(tx *repositories.Query) error {
		var acceptErr error

		result, acceptErr = s.acceptChildInboxMessage(ctx, tx, sessionID,
			executionID, input, parts)

		return acceptErr
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "accept child inbox transaction")
	}

	ctxscope.GetLogger(ctx).Info("child execution message accepted",
		"session_id", sessionID, "execution_id", executionID,
		"request_id", result.Message.RequestID)

	return result, nil
}

func (s *Store) acceptChildInboxMessage(
	ctx context.Context,
	tx *repositories.Query,
	sessionID, executionID uuid.UUID,
	input AcceptExecutionMessageInput,
	parts string,
) (*ExecutionInboxAcceptance, error) {
	execution, err := s.findExecution(ctx, tx, sessionID, executionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read child inbox execution")
	}

	if execution.Kind != models.ExecutionKindChild ||
		execution.AgentRunID == nil {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"inbox execution is not a child")
	}

	existing, err := findInboxRequest(ctx, tx, sessionID, input.RequestID)
	if err != nil && !errors.Is(err, commerr.ErrNotFound) {
		return nil, ctxerrors.Wrap(err, "read child acceptance identity")
	}

	if existing != nil {
		if !sameInboxInput(existing, executionID, input, parts) {
			return nil, ctxerrors.Wrap(commerr.ErrConflict,
				"child inbox request changed")
		}

		return &ExecutionInboxAcceptance{Message: existing}, nil
	}

	if isExecutionTerminal(execution.State) {
		return nil, ctxerrors.Wrap(
			errors.Join(commerr.ErrConflict, ErrAgentRunFinished),
			"child execution has finished")
	}

	if execution.State == models.ExecutionStatePaused {
		return s.resumeChildWithMessage(ctx, tx, execution, input, parts)
	}

	return s.insertChildInboxMessage(ctx, tx, execution, input, parts)
}

// resumeChildWithMessage resumes a paused child and the paused work under it,
// then queues the resume notice and the message, all in the caller's
// transaction. A message is how a person or an agent continues a child a
// Stop, a crash, or a failure paused.
func (s *Store) resumeChildWithMessage(
	ctx context.Context,
	tx *repositories.Query,
	execution *models.Execution,
	input AcceptExecutionMessageInput,
	parts string,
) (*ExecutionInboxAcceptance, error) {
	payload, err := json.Marshal(struct {
		RequestID uuid.UUID `json:"requestId"`
	}{input.RequestID})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode message resume provenance")
	}

	resumed, err := s.changeExecutionSubtree(ctx, tx, execution.SessionID,
		execution.ID, ExecutionEventTypeResumed, "", payload)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resume child for message")
	}

	// The resume advanced the child's event count, so read it again.
	execution, err = s.findExecution(ctx, tx, execution.SessionID,
		execution.ID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read resumed child")
	}

	if strings.TrimSpace(input.ResumeNotice) != "" {
		notice := AcceptExecutionMessageInput{
			RequestID: uuid.NewSHA1(input.RequestID,
				[]byte(resumeNoticeIdentity)),
			WorkingDirectory: input.WorkingDirectory,
			Message:          input.ResumeNotice,
			Capacity:         input.Capacity,
		}

		if _, err := s.insertChildInboxMessage(ctx, tx, execution, notice,
			emptyChildReportPartsJSON); err != nil {
			return nil, ctxerrors.Wrap(err, "queue child resume notice")
		}
	}

	accepted, err := s.insertChildInboxMessage(ctx, tx, execution, input,
		parts)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "queue resuming child message")
	}

	accepted.Resumed = resumed

	return accepted, nil
}

func (s *Store) insertChildInboxMessage(
	ctx context.Context,
	tx *repositories.Query,
	execution *models.Execution,
	input AcceptExecutionMessageInput,
	parts string,
) (*ExecutionInboxAcceptance, error) {
	if execution.EventCount == math.MaxInt64 {
		return nil, ctxerrors.Wrap(commerr.ErrInvalidState,
			"child inbox event sequence is exhausted")
	}

	repository := tx.ExecutionInboxMessage

	used, err := repository.WithContext(ctx).QueuedSlots(
		execution.SessionID.String(), execution.ID.String(),
		string(models.InboxMessageStateQueued))
	if err != nil {
		return nil, ctxerrors.Wrap(err, "count queued child message slots")
	}

	if used > int64(input.Capacity)-1-int64(len(input.Parts)) {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"child inbox capacity is exhausted")
	}

	sequence, err := nextInboxSequence(ctx, tx, execution.ID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "allocate child inbox sequence")
	}

	message := &models.ExecutionInboxMessage{
		ID: s.newID(), SessionID: execution.SessionID,
		ExecutionID: execution.ID,
		TurnID:      execution.RootTurnID, RequestID: input.RequestID,
		Sequence: sequence, WorkingDirectory: input.WorkingDirectory,
		Message: input.Message, PartsJSON: parts,
		State: models.InboxMessageStateQueued, CreatedAt: s.now(),
	}
	if input.SourceEventID != uuid.Nil {
		message.SourceEventID = &input.SourceEventID
	}

	if err := repository.WithContext(ctx).Create(message); err != nil {
		return nil, ctxerrors.Wrap(err, "save accepted child inbox message")
	}

	if err := s.recordChildInboxAcceptance(ctx, tx, execution,
		message); err != nil {
		return nil, ctxerrors.Wrap(err, "record child inbox acceptance")
	}

	return &ExecutionInboxAcceptance{Message: message}, nil
}

func (s *Store) recordChildInboxAcceptance(
	ctx context.Context,
	tx *repositories.Query,
	execution *models.Execution,
	message *models.ExecutionInboxMessage,
) error {
	payload := struct {
		InboxID    uuid.UUID `json:"inboxId"`
		RequestID  uuid.UUID `json:"requestId"`
		AgentRunID uuid.UUID `json:"agentRunId"`
	}{message.ID, message.RequestID, *execution.AgentRunID}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return ctxerrors.Wrap(err, "encode child inbox acceptance identity")
	}

	return s.appendExecutionEvent(ctx, tx, execution,
		ExecutionEventTypeInboxQueued, encoded)
}
