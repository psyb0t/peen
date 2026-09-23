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

// AppendAgentRunMessages stores prompt-visible child records in one
// transaction, so a child loop never continues past a message the durable
// transcript does not hold.
//
// Sequences are allocated per agent run, independent of the session
// transcript. A child agent is a separate model context, so its messages never
// enter the session transcript and a parent-session compaction can never
// absorb them.
func (s *Store) AppendAgentRunMessages(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	inputs []AgentRunMessageInput,
) ([]*models.AgentRunMessage, error) {
	if len(inputs) == 0 {
		return nil, nil
	}

	for index, input := range inputs {
		if err := validateAgentRunMessageInput(input); err != nil {
			return nil, ctxerrors.Wrapf(
				err,
				"validate child message %d",
				index,
			)
		}
	}

	var stored []*models.AgentRunMessage

	if err := s.query.Transaction(func(tx *repositories.Query) error {
		created, err := s.createAgentRunMessages(
			ctx,
			tx,
			sessionID,
			agentRunID,
			inputs,
		)
		if err != nil {
			return err
		}

		stored = created

		return nil
	}); err != nil {
		return nil, ctxerrors.Wrap(err, "append child messages transaction")
	}

	return stored, nil
}

func (s *Store) createAgentRunMessages(
	ctx context.Context,
	tx *repositories.Query,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	inputs []AgentRunMessageInput,
) ([]*models.AgentRunMessage, error) {
	if _, err := s.findAgentRunWithQuery(
		ctx,
		tx,
		sessionID,
		agentRunID,
	); err != nil {
		return nil, ctxerrors.Wrap(err, "find agent run for message")
	}

	sequence, err := nextAgentRunMessageSequence(ctx, tx, agentRunID)
	if err != nil {
		return nil, err
	}

	stored := make([]*models.AgentRunMessage, 0, len(inputs))

	for _, input := range inputs {
		message := s.newAgentRunMessage(
			sessionID,
			agentRunID,
			sequence,
			input,
		)

		if err := tx.AgentRunMessage.WithContext(ctx).
			Create(message); err != nil {
			return nil, ctxerrors.Wrap(err, "create child message")
		}

		stored = append(stored, message)
		sequence++
	}

	return stored, nil
}

// requireAgentRun rejects a read whose agent run does not belong to the
// session, so a caller cannot reach another session's child records.
func (s *Store) requireAgentRun(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	purpose string,
) error {
	_, err := s.findAgentRunWithQuery(ctx, s.query, sessionID, agentRunID)
	if err != nil {
		return ctxerrors.Wrapf(err, "find agent run for %s", purpose)
	}

	return nil
}

func (s *Store) newAgentRunMessage(
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	sequence int64,
	input AgentRunMessageInput,
) *models.AgentRunMessage {
	id := input.ID
	if id == uuid.Nil {
		id = s.newID()
	}

	toolCalls := input.ToolCallsJSON
	if toolCalls == "" {
		toolCalls = defaultToolCallsJSON
	}

	return &models.AgentRunMessage{
		ID:            id,
		SessionID:     sessionID,
		AgentRunID:    agentRunID,
		Sequence:      sequence,
		Role:          input.Role,
		Content:       input.Content,
		ModelID:       input.ModelID,
		Thinking:      input.Thinking,
		ToolCallsJSON: toolCalls,
		ToolCallID:    input.ToolCallID,
		IsError:       input.IsError,
		Incomplete:    input.Incomplete,
		CreatedAt:     s.now(),
	}
}

func validateAgentRunMessageInput(input AgentRunMessageInput) error {
	switch input.Role {
	case models.MessageRoleUser,
		models.MessageRoleAssistant,
		models.MessageRoleTool:
	default:
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"child message role",
		)
	}

	return nil
}

func nextAgentRunMessageSequence(
	ctx context.Context,
	query *repositories.Query,
	agentRunID uuid.UUID,
) (int64, error) {
	message := query.AgentRunMessage

	return nextSequence(func() (int64, error) {
		latest, err := message.WithContext(ctx).
			Where(message.AgentRunID.Eq(agentRunID)).
			Order(message.Sequence.Desc(), message.ID.Desc()).
			First()
		if err != nil {
			return 0, ctxerrors.Wrap(err, "query latest child message")
		}

		return latest.Sequence, nil
	})
}

// ListAgentRunMessages returns one bounded page of a child's own transcript,
// oldest first, scoped to the owning session.
func (s *Store) ListAgentRunMessages(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	options ListAgentRunMessagesOptions,
) (*AgentRunMessagePage, error) {
	page, err := normalizeReadPage(options.Limit, options.Offset)
	if err != nil {
		return nil, err
	}

	if err := s.requireAgentRun(
		ctx,
		sessionID,
		agentRunID,
		"message listing",
	); err != nil {
		return nil, err
	}

	message := s.query.AgentRunMessage
	query := message.WithContext(ctx).
		Where(
			message.SessionID.Eq(sessionID),
			message.AgentRunID.Eq(agentRunID),
		).
		Order(message.Sequence, message.ID)

	items, hasMore, err := listReadPage(
		page,
		func(offset, limit int) ([]*models.AgentRunMessage, error) {
			return query.Offset(offset).Limit(limit).Find()
		},
		"list child messages",
		"probe child message page",
	)
	if err != nil {
		return nil, err
	}

	return &AgentRunMessagePage{
		Items:   items,
		Limit:   page.limit,
		Offset:  page.offset,
		HasMore: hasMore,
	}, nil
}

// AgentRunHistory returns the child's current summary and the raw tail that
// follows it, which is what rebuilds a child conversation after a restart.
func (s *Store) AgentRunHistory(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
) (*AgentRunHistoryResult, error) {
	compaction, err := s.LatestAgentRunCompaction(ctx, sessionID, agentRunID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get latest child compaction")
	}

	var afterSequence int64
	if compaction != nil {
		afterSequence = compaction.ToSequence
	}

	message := s.query.AgentRunMessage

	messages, err := message.WithContext(ctx).
		Where(
			message.AgentRunID.Eq(agentRunID),
			message.Sequence.Gt(afterSequence),
		).
		Order(message.Sequence, message.ID).
		Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get child message history")
	}

	return &AgentRunHistoryResult{
		Compaction: compaction,
		Messages:   messages,
	}, nil
}

// CreateAgentRunCompaction stores one immutable child summary and assigns the
// direct messages it covered.
//
// Only rows this compaction covered itself are assigned, so an earlier child
// compaction keeps the direct membership it already recorded.
func (s *Store) CreateAgentRunCompaction(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	input AgentRunCompactionInput,
) (*models.AgentRunCompaction, error) {
	normalized, err := normalizeAgentRunCompactionInput(input)
	if err != nil {
		return nil, err
	}

	if normalized.ID == uuid.Nil {
		normalized.ID = s.newID()
	}

	compaction := &models.AgentRunCompaction{
		ID:                     normalized.ID,
		SessionID:              sessionID,
		AgentRunID:             agentRunID,
		FromMessageID:          normalized.FromMessageID,
		ToMessageID:            normalized.ToMessageID,
		FromSequence:           normalized.FromSequence,
		ToSequence:             normalized.ToSequence,
		DirectFromSequence:     normalized.DirectFromSequence,
		DirectToSequence:       normalized.DirectToSequence,
		Summary:                normalized.Summary,
		SourceMessageCount:     normalized.SourceMessageCount,
		InputTokenCount:        normalized.InputTokenCount,
		SummaryTokenCount:      normalized.SummaryTokenCount,
		ModelID:                normalized.ModelID,
		PromptHash:             normalized.PromptHash,
		CreatedAt:              s.now(),
		SupersedesCompactionID: normalized.SupersedesCompactionID,
	}

	if err := s.query.Transaction(func(tx *repositories.Query) error {
		if _, err := s.findAgentRunWithQuery(
			ctx,
			tx,
			sessionID,
			agentRunID,
		); err != nil {
			return ctxerrors.Wrap(err, "find agent run for compaction")
		}

		return s.createAgentRunCompaction(ctx, tx, compaction, normalized)
	}); err != nil {
		return nil, ctxerrors.Wrap(
			err,
			"create child compaction transaction",
		)
	}

	return compaction, nil
}

func (s *Store) createAgentRunCompaction(
	ctx context.Context,
	query *repositories.Query,
	compaction *models.AgentRunCompaction,
	input AgentRunCompactionInput,
) error {
	if err := query.AgentRunCompaction.WithContext(ctx).
		Create(compaction); err != nil {
		return ctxerrors.Wrap(err, "create child compaction")
	}

	message := query.AgentRunMessage

	assigned, err := message.WithContext(ctx).
		Where(
			message.AgentRunID.Eq(compaction.AgentRunID),
			message.Sequence.Between(
				input.DirectFromSequence,
				input.DirectToSequence,
			),
			message.CompactionID.IsNull(),
		).
		UpdateSimple(message.CompactionID.Value(compaction.ID))
	if err != nil {
		return ctxerrors.Wrap(err, "assign direct child message compaction")
	}

	if assigned.RowsAffected == 0 {
		return ctxerrors.Wrap(
			commerr.ErrInvalidState,
			"child compaction has no unassigned direct messages",
		)
	}

	return nil
}

func normalizeAgentRunCompactionInput(
	input AgentRunCompactionInput,
) (AgentRunCompactionInput, error) {
	if input.FromSequence <= 0 || input.ToSequence < input.FromSequence {
		return AgentRunCompactionInput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"invalid child compaction sequence bounds",
		)
	}

	if input.DirectFromSequence == 0 {
		input.DirectFromSequence = input.FromSequence
	}

	if input.DirectToSequence == 0 {
		input.DirectToSequence = input.ToSequence
	}

	if input.DirectFromSequence < input.FromSequence ||
		input.DirectToSequence < input.DirectFromSequence ||
		input.DirectToSequence > input.ToSequence {
		return AgentRunCompactionInput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"invalid direct child compaction sequence bounds",
		)
	}

	if input.Summary == "" || input.SourceMessageCount <= 0 {
		return AgentRunCompactionInput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"child compaction summary",
		)
	}

	return input, nil
}

// LatestAgentRunCompaction returns the child's current chain head, never a
// superseded row.
func (s *Store) LatestAgentRunCompaction(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
) (*models.AgentRunCompaction, error) {
	if err := s.requireAgentRun(
		ctx,
		sessionID,
		agentRunID,
		"compaction head",
	); err != nil {
		return nil, err
	}

	compaction, err := s.query.AgentRunCompaction.WithContext(ctx).
		ActiveHead(agentRunID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		//nolint:nilnil // A nil compaction denotes normal summary absence.
		return nil, nil
	}

	if err != nil {
		return nil, ctxerrors.Wrap(err, "get current child compaction")
	}

	return compaction, nil
}

// ListAgentRunCompactions returns one bounded page of a child's compaction
// lineage, newest first, scoped to the owning session.
func (s *Store) ListAgentRunCompactions(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	options ListAgentRunCompactionsOptions,
) (*AgentRunCompactionPage, error) {
	page, err := normalizeReadPage(options.Limit, options.Offset)
	if err != nil {
		return nil, err
	}

	if err := s.requireAgentRun(
		ctx,
		sessionID,
		agentRunID,
		"compaction listing",
	); err != nil {
		return nil, err
	}

	compaction := s.query.AgentRunCompaction
	query := compaction.WithContext(ctx).
		Where(
			compaction.SessionID.Eq(sessionID),
			compaction.AgentRunID.Eq(agentRunID),
		).
		Order(compaction.CreatedAt.Desc(), compaction.ID.Desc())

	items, hasMore, err := listReadPage(
		page,
		func(offset, limit int) ([]*models.AgentRunCompaction, error) {
			return query.Offset(offset).Limit(limit).Find()
		},
		"list child compactions",
		"probe child compaction page",
	)
	if err != nil {
		return nil, err
	}

	return &AgentRunCompactionPage{
		Items:   items,
		Limit:   page.limit,
		Offset:  page.offset,
		HasMore: hasMore,
	}, nil
}

// GetAgentRunCompaction returns one immutable child compaction owned by both
// the session and the agent run.
func (s *Store) GetAgentRunCompaction(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	compactionID uuid.UUID,
) (*models.AgentRunCompaction, error) {
	if err := s.requireAgentRun(
		ctx,
		sessionID,
		agentRunID,
		"compaction read",
	); err != nil {
		return nil, err
	}

	compaction := s.query.AgentRunCompaction

	stored, err := compaction.WithContext(ctx).
		Where(
			compaction.ID.Eq(compactionID),
			compaction.SessionID.Eq(sessionID),
			compaction.AgentRunID.Eq(agentRunID),
		).
		First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get child compaction")
	}

	return stored, nil
}
