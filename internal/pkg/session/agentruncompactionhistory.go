package session

import (
	"context"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"gorm.io/gorm"
)

func validateAgentRunCompactionHistory(
	ctx context.Context,
	query *repositories.Query,
	compaction *models.AgentRunCompaction,
	input AgentRunCompactionInput,
) error {
	head, err := query.AgentRunCompaction.WithContext(ctx).
		ActiveHead(compaction.AgentRunID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return ctxerrors.Wrap(err, "read child compaction predecessor")
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		head = nil
	}

	priorCount, afterSequence, err := agentRunCompactionPredecessor(head, input)
	if err != nil {
		return ctxerrors.Wrap(err, "validate child compaction predecessor")
	}

	repository := query.AgentRunMessage
	scoped := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(compaction.SessionID),
		repository.AgentRunID.Eq(compaction.AgentRunID),
		repository.Incomplete.Is(false),
	)

	skipped, err := scoped.Where(repository.Sequence.Gt(afterSequence),
		repository.Sequence.Lt(input.DirectFromSequence)).Count()
	if err != nil {
		return ctxerrors.Wrap(err, "count skipped child history")
	}

	if skipped != 0 {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"child compaction skips completed history")
	}

	messages, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(compaction.SessionID),
		repository.AgentRunID.Eq(compaction.AgentRunID),
		repository.Incomplete.Is(false),
		repository.Sequence.Between(input.DirectFromSequence,
			input.DirectToSequence),
	).Order(repository.Sequence).Find()
	if err != nil {
		return ctxerrors.Wrap(err, "read direct child compaction history")
	}

	return validateAgentRunCompactionMessages(messages, input, priorCount)
}

func agentRunCompactionPredecessor(
	head *models.AgentRunCompaction,
	input AgentRunCompactionInput,
) (int64, int64, error) {
	if head == nil {
		if input.SupersedesCompactionID != nil ||
			input.DirectFromSequence != input.FromSequence ||
			input.DirectToSequence != input.ToSequence {
			return 0, 0, ctxerrors.Wrap(commerr.ErrConflict,
				"child compaction predecessor is missing")
		}

		return 0, 0, nil
	}

	if invalidAgentRunCompactionPredecessor(head, input) {
		return 0, 0, ctxerrors.Wrap(commerr.ErrConflict,
			"child compaction predecessor changed")
	}

	return head.SourceMessageCount, head.ToSequence, nil
}

func invalidAgentRunCompactionPredecessor(
	head *models.AgentRunCompaction,
	input AgentRunCompactionInput,
) bool {
	return input.SupersedesCompactionID == nil ||
		*input.SupersedesCompactionID != head.ID ||
		input.FromMessageID != head.FromMessageID ||
		input.FromSequence != head.FromSequence ||
		input.DirectFromSequence <= head.ToSequence ||
		input.DirectToSequence != input.ToSequence
}

func validateAgentRunCompactionMessages(
	messages []*models.AgentRunMessage,
	input AgentRunCompactionInput,
	priorCount int64,
) error {
	if len(messages) == 0 ||
		int64(len(messages)) != input.SourceMessageCount-priorCount {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"child compaction source count differs")
	}

	first, last := messages[0], messages[len(messages)-1]
	if first.Sequence != input.DirectFromSequence ||
		last.Sequence != input.DirectToSequence ||
		last.ID != input.ToMessageID ||
		(priorCount == 0 && first.ID != input.FromMessageID) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"child compaction message bounds differ")
	}

	for _, message := range messages {
		if message.CompactionID != nil {
			return ctxerrors.Wrap(commerr.ErrConflict,
				"child compaction message is already assigned")
		}
	}

	return nil
}
