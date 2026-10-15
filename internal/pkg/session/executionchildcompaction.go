package session

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

func (s *Store) saveChildActivityCompaction(
	ctx context.Context,
	query *repositories.Query,
	activity *models.ExecutionActivity,
	execution *models.Execution,
	input CompleteActivityInput,
) error {
	if activity.Kind != models.ActivityKindCompaction || input.Finish != nil ||
		execution.AgentRunID == nil || *execution.AgentRunID == uuid.Nil {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"compaction requires a child compaction activity")
	}

	normalized, err := s.validateActivityCompaction(*input.Compaction,
		activity.ID)
	if err != nil {
		return ctxerrors.Wrap(err, "validate child compaction completion")
	}

	childInput := AgentRunCompactionInput(normalized)

	row := s.agentRunCompactionRecord(execution.SessionID,
		*execution.AgentRunID, childInput)
	if err := s.createAgentRunCompaction(ctx, query, row,
		childInput); err != nil {
		return ctxerrors.Wrap(err, "commit child activity summary")
	}

	return saveCompactionIdentity(ctx, query, activity, input.Compaction)
}

func (s *Store) agentRunCompactionRecord(
	sessionID, agentRunID uuid.UUID,
	input AgentRunCompactionInput,
) *models.AgentRunCompaction {
	return &models.AgentRunCompaction{
		ID: input.ID, SessionID: sessionID, AgentRunID: agentRunID,
		FromMessageID: input.FromMessageID, ToMessageID: input.ToMessageID,
		FromSequence: input.FromSequence, ToSequence: input.ToSequence,
		DirectFromSequence: input.DirectFromSequence,
		DirectToSequence:   input.DirectToSequence,
		Summary:            input.Summary,
		SourceMessageCount: input.SourceMessageCount,
		InputTokenCount:    input.InputTokenCount,
		SummaryTokenCount:  input.SummaryTokenCount,
		ModelID:            input.ModelID, PromptHash: input.PromptHash,
		CreatedAt:              s.now(),
		SupersedesCompactionID: input.SupersedesCompactionID,
	}
}
