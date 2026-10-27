package session

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"gorm.io/gorm"
)

func validateCompactionHistory(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	input CompactionInput,
) ([]uuid.UUID, error) {
	if err := validateCompactionBoundary(ctx, query, sessionID,
		input.FromMessageID, input.FromSequence); err != nil {
		return nil, ctxerrors.Wrap(err, "validate first compaction boundary")
	}

	if err := validateCompactionBoundary(ctx, query, sessionID,
		input.ToMessageID, input.ToSequence); err != nil {
		return nil, ctxerrors.Wrap(err, "validate last compaction boundary")
	}

	priorCount, afterSequence, err := validateCompactionChain(ctx, query,
		sessionID, input)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "validate compaction chain")
	}

	if err := validateCompactionPrefix(ctx, query, sessionID,
		afterSequence, input.DirectFromSequence); err != nil {
		return nil, ctxerrors.Wrap(err, "validate compaction prefix")
	}

	messages, err := readDirectCompactionMessages(ctx, query, sessionID,
		input, priorCount)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read compaction history")
	}

	if invalidCompactionBounds(messages, input, priorCount) {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"compaction history bounds or count differ")
	}

	if priorCount == 0 && messages[0].ID != input.FromMessageID {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"compaction first message differs")
	}

	return validatedCompactionIDs(ctx, query, sessionID, messages)
}

func validatedCompactionIDs(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	messages []*models.Message,
) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, 0, len(messages))
	for _, message := range messages {
		if err := validateCompactionMessage(ctx, query, sessionID,
			message); err != nil {
			return nil, ctxerrors.Wrap(err, "validate compaction message")
		}

		ids = append(ids, message.ID)
	}

	return ids, nil
}

func validateCompactionBoundary(
	ctx context.Context,
	query *repositories.Query,
	sessionID, messageID uuid.UUID,
	sequence int64,
) error {
	repository := query.Message

	message, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(sessionID), repository.ID.Eq(messageID),
	).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"compaction boundary is missing from this session")
	}

	if err != nil {
		return ctxerrors.Wrap(err, "read compaction boundary")
	}

	if message.Sequence != sequence {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"compaction boundary sequence differs")
	}

	return validateCompactionBoundaryTurn(ctx, query, sessionID, message)
}

func validateCompactionBoundaryTurn(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	message *models.Message,
) error {
	repository := query.Turn

	turn, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(sessionID), repository.ID.Eq(message.TurnID),
	).First()
	if err != nil {
		return ctxerrors.Wrap(err, "read compaction boundary turn")
	}

	if turn.State != models.TurnStateCompleted || message.Incomplete {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"compaction boundary is unfinished")
	}

	return nil
}

func validateCompactionChain(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	input CompactionInput,
) (int64, int64, error) {
	head, err := query.Compaction.WithContext(ctx).ActiveHead(sessionID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, 0, ctxerrors.Wrap(err, "read compaction chain head")
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		if input.SupersedesCompactionID != nil ||
			input.DirectFromSequence != input.FromSequence ||
			input.DirectToSequence != input.ToSequence {
			return 0, 0, ctxerrors.Wrap(commerr.ErrConflict,
				"compaction predecessor is missing")
		}

		return 0, 0, nil
	}

	if invalidCompactionPredecessor(head, input) {
		return 0, 0, ctxerrors.Wrap(commerr.ErrConflict,
			"compaction predecessor changed")
	}

	return head.SourceMessageCount, head.ToSequence, nil
}

func validateCompactionPrefix(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	afterSequence, beforeSequence int64,
) error {
	count, err := query.Message.WithContext(ctx).CountCompletedHistoryBefore(
		sessionID, afterSequence, beforeSequence,
	)
	if err != nil {
		return ctxerrors.Wrap(err, "count skipped completed history")
	}

	if count != 0 {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"compaction skips completed history")
	}

	return nil
}

func validateCompactionMessage(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	message *models.Message,
) error {
	if message.CompactionID != nil {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"compaction message is already replaced")
	}

	repository := query.Turn

	turn, err := repository.WithContext(ctx).Where(
		repository.ID.Eq(message.TurnID),
		repository.SessionID.Eq(sessionID),
	).First()
	if err != nil {
		return ctxerrors.Wrap(err, "read compacted message turn")
	}

	if turn.State == models.TurnStateRunning {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"compaction message turn is still running")
	}

	return nil
}

func invalidCompactionBounds(
	messages []*models.Message,
	input CompactionInput,
	priorCount int64,
) bool {
	return len(messages) == 0 ||
		messages[0].Sequence != input.DirectFromSequence ||
		messages[len(messages)-1].Sequence != input.DirectToSequence ||
		messages[len(messages)-1].ID != input.ToMessageID ||
		int64(len(messages)) != input.SourceMessageCount-priorCount
}

func invalidCompactionPredecessor(
	head *models.Compaction,
	input CompactionInput,
) bool {
	return input.SupersedesCompactionID == nil ||
		*input.SupersedesCompactionID != head.ID ||
		input.FromMessageID != head.FromMessageID ||
		input.FromSequence != head.FromSequence ||
		input.DirectFromSequence <= head.ToSequence ||
		input.DirectToSequence != input.ToSequence
}

func readDirectCompactionMessages(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	input CompactionInput,
	priorCount int64,
) ([]*models.Message, error) {
	repository := query.Message

	count, err := repository.WithContext(ctx).CountCompletedHistoryRange(
		sessionID, input.DirectFromSequence, input.DirectToSequence,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "count direct compaction messages")
	}

	if count < 1 || count != input.SourceMessageCount-priorCount {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"compaction source count differs")
	}

	messages, err := repository.WithContext(ctx).CompletedHistoryRange(
		sessionID, input.DirectFromSequence, input.DirectToSequence,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read direct compaction messages")
	}

	return messages, nil
}
