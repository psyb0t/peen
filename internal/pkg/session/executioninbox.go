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
	"gorm.io/gorm"
)

// AcceptExecutionMessage retains the accepted text and skill content by ID.
func (s *Store) AcceptExecutionMessage(
	ctx context.Context,
	executionID uuid.UUID,
	lease Lease,
	input AcceptExecutionMessageInput,
) (*ExecutionInboxAcceptance, error) {
	return s.acceptExecutionInbox(ctx, executionID, lease, input,
		models.InboxMessageStateQueued)
}

func (s *Store) acceptExecutionInbox(
	ctx context.Context,
	executionID uuid.UUID,
	lease Lease,
	input AcceptExecutionMessageInput,
	state models.InboxMessageState,
) (*ExecutionInboxAcceptance, error) {
	parts, err := s.validateExecutionInboxInput(input)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "validate accepted inbox message")
	}

	unlock, err := s.holdLease(lease)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "hold inbox acceptance turn")
	}
	defer unlock()

	var result *ExecutionInboxAcceptance

	err = s.feedTransaction(ctx, func(
		ctx context.Context,
		tx *repositories.Query,
	) error {
		var acceptErr error

		result, acceptErr = s.acceptExecutionMessage(ctx, tx, executionID,
			lease, input, parts, state)

		return acceptErr
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "accept inbox message transaction")
	}

	ctxscope.GetLogger(ctx).Info("execution inbox message accepted",
		"execution_id", executionID, "request_id", result.Message.RequestID,
		"inbox_id", result.Message.ID)

	return result, nil
}

// fitsInboxStorage reports whether the message and its resume notice each fit
// one stored inbox row.
func (s *Store) fitsInboxStorage(input AcceptExecutionMessageInput) bool {
	return len(input.Message) <= s.maxStoredMessageBytes &&
		len(input.ResumeNotice) <= s.maxStoredMessageBytes
}

func (s *Store) validateExecutionInboxInput(
	input AcceptExecutionMessageInput,
) (string, error) {
	if input.RequestID == uuid.Nil || input.Capacity <= 0 ||
		strings.TrimSpace(input.WorkingDirectory) == "" ||
		strings.TrimSpace(input.Message) == "" ||
		!s.fitsInboxStorage(input) {
		return "", ctxerrors.Wrap(commerr.ErrValidationFailed,
			"accepted inbox identity or message")
	}

	if err := validateExecutionInboxParts(input.Parts); err != nil {
		return "", ctxerrors.Wrap(err, "validate accepted inbox skills")
	}

	parts := input.Parts
	if parts == nil {
		parts = []ExecutionInboxPart{}
	}

	encoded, err := json.Marshal(parts)
	if err != nil {
		return "", ctxerrors.Wrap(err, "encode accepted inbox skills")
	}

	if len(encoded) > s.maxStoredMessageBytes {
		return "", ctxerrors.Wrap(commerr.ErrValidationFailed,
			"accepted inbox skills exceed storage limit")
	}

	return string(encoded), nil
}

func (s *Store) acceptExecutionMessage(
	ctx context.Context,
	tx *repositories.Query,
	executionID uuid.UUID,
	lease Lease,
	input AcceptExecutionMessageInput,
	parts string,
	state models.InboxMessageState,
) (*ExecutionInboxAcceptance, error) {
	execution, err := s.findExecution(ctx, tx, lease.SessionID, executionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "find accepted inbox execution")
	}

	if execution.Kind != models.ExecutionKindRoot ||
		execution.RootTurnID != lease.TurnID {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"accepted inbox belongs to another root turn")
	}

	existing, err := findInboxRequest(ctx, tx, lease.SessionID, input.RequestID)
	if err != nil && !errors.Is(err, commerr.ErrNotFound) {
		return nil, ctxerrors.Wrap(err, "read accepted inbox identity")
	}

	if existing != nil {
		if err := validateInboxReplay(existing, executionID, input,
			parts, state); err != nil {
			return nil, ctxerrors.Wrap(err, "validate inbox replay")
		}

		return &ExecutionInboxAcceptance{Message: existing}, nil
	}

	if err := validateInboxAdmission(ctx, tx, execution, state); err != nil {
		return nil, ctxerrors.Wrap(err, "validate inbox admission")
	}

	return s.insertInboxMessage(ctx, tx, execution, lease, input, parts, state)
}

func validateInboxReplay(
	existing *models.ExecutionInboxMessage,
	executionID uuid.UUID,
	input AcceptExecutionMessageInput,
	parts string,
	state models.InboxMessageState,
) error {
	isDeferred := existing.State == models.InboxMessageStateDeferred ||
		existing.State == models.InboxMessageStateForwarded
	if isDeferred != (state == models.InboxMessageStateDeferred) ||
		!sameInboxInput(existing, executionID, input, parts) {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"accepted inbox input or delivery mode differs")
	}

	return nil
}

func validateInboxAdmission(
	ctx context.Context,
	query *repositories.Query,
	execution *models.Execution,
	state models.InboxMessageState,
) error {
	if execution.State == models.ExecutionStatePaused ||
		isExecutionTerminal(execution.State) {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"execution cannot accept another inbox message")
	}

	if state == models.InboxMessageStateDeferred {
		return validateDeferredBoundary(ctx, query, execution)
	}

	return nil
}

func findInboxRequest(
	ctx context.Context,
	tx *repositories.Query,
	sessionID, requestID uuid.UUID,
) (*models.ExecutionInboxMessage, error) {
	repository := tx.ExecutionInboxMessage

	message, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(sessionID),
		repository.RequestID.Eq(requestID),
	).First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "find inbox request")
	}

	return message, nil
}

func sameInboxInput(
	message *models.ExecutionInboxMessage,
	executionID uuid.UUID,
	input AcceptExecutionMessageInput,
	parts string,
) bool {
	return message.ExecutionID == executionID &&
		message.Message == input.Message &&
		message.WorkingDirectory == input.WorkingDirectory &&
		message.PartsJSON == parts &&
		sameOptionalID(message.SourceEventID, input.SourceEventID)
}

func sameOptionalID(saved *uuid.UUID, requested uuid.UUID) bool {
	if saved == nil {
		return requested == uuid.Nil
	}

	return *saved == requested
}

func (s *Store) insertInboxMessage(
	ctx context.Context,
	tx *repositories.Query,
	execution *models.Execution,
	lease Lease,
	input AcceptExecutionMessageInput,
	parts string,
	state models.InboxMessageState,
) (*ExecutionInboxAcceptance, error) {
	revision, err := s.admissionStopRevision(ctx, tx, lease.SessionID,
		input.ExpectedStopRevision)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "validate inbox stop revision")
	}

	repository := tx.ExecutionInboxMessage

	used, err := repository.WithContext(ctx).QueuedSlots(
		lease.SessionID.String(), execution.ID.String(),
		string(state))
	if err != nil {
		return nil, ctxerrors.Wrap(err, "count queued inbox slots")
	}

	if used > int64(input.Capacity)-1-int64(len(input.Parts)) {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"execution inbox capacity is exhausted")
	}

	sequence, err := nextInboxSequence(ctx, tx, execution.ID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "allocate inbox sequence")
	}

	message := &models.ExecutionInboxMessage{
		ID: s.newID(), SessionID: lease.SessionID, ExecutionID: execution.ID,
		TurnID: lease.TurnID, RequestID: input.RequestID, Sequence: sequence,
		WorkingDirectory: input.WorkingDirectory, Message: input.Message,
		PartsJSON: parts, State: state, StopRevision: revision,
		CreatedAt: s.now(),
	}
	if input.SourceEventID != uuid.Nil {
		message.SourceEventID = &input.SourceEventID
	}

	if err := repository.WithContext(ctx).Create(message); err != nil {
		return nil, ctxerrors.Wrap(err, "save accepted inbox message")
	}

	events, err := s.inboxAcceptanceEvents(ctx, tx, lease, message)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build inbox acceptance events")
	}

	_, err = s.appendTranscript(ctx, tx, lease, nil, events)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "save inbox acceptance events")
	}

	return &ExecutionInboxAcceptance{Message: message, Events: events}, nil
}

func nextInboxSequence(
	ctx context.Context,
	tx *repositories.Query,
	executionID uuid.UUID,
) (int64, error) {
	repository := tx.ExecutionInboxMessage

	latest, err := repository.WithContext(ctx).
		Where(repository.ExecutionID.Eq(executionID)).
		Order(repository.Sequence.Desc()).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 1, nil
	}

	if err != nil {
		return 0, ctxerrors.Wrap(err, "read latest inbox sequence")
	}

	if latest.Sequence == math.MaxInt64 {
		return 0, ctxerrors.Wrap(commerr.ErrInvalidState,
			"inbox sequence exceeds integer capacity")
	}

	return latest.Sequence + 1, nil
}

func validateExecutionInboxParts(parts []ExecutionInboxPart) error {
	for _, part := range parts {
		if strings.TrimSpace(part.SkillName) == "" || part.Content == "" {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"accepted inbox skill part")
		}
	}

	return nil
}
