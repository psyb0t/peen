package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const (
	childMessagePageKind    = "child message"
	childCompactionPageKind = "child compaction"

	childPageLimitField  = "%s page limit"
	childPageOffsetField = "%s page offset"
)

// ListSessionAgentRunMessages returns one child agent run's own durable
// transcript, oldest first, with the same bounded limit/offset paging the
// session transcript uses.
//
// The rows are the child's, never the session's, so a caller reading them can
// never see parent transcript records through this route.
func (r *Runtime) ListSessionAgentRunMessages(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	params api.ListSessionAgentRunMessagesParams,
) (*api.AgentRunMessagePage, error) {
	limit, offset, err := pagingOptionsFromAPI(params.Limit, params.Offset)
	if err != nil {
		return nil, err
	}

	stored, err := r.store.ListAgentRunMessages(
		ctx,
		sessionID,
		agentRunID,
		session.ListAgentRunMessagesOptions{Limit: limit, Offset: offset},
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list durable child messages")
	}

	converted, err := convertAgentRunPage(
		stored.Items,
		stored.Limit,
		stored.Offset,
		stored.HasMore,
		childMessagePageKind,
		agentRunMessageToAPI,
	)
	if err != nil {
		return nil, err
	}

	return &api.AgentRunMessagePage{
		Messages: converted.Items,
		HasMore:  converted.HasMore,
		Limit:    converted.Limit,
		Offset:   converted.Offset,
	}, nil
}

// ListSessionAgentRunCompactions returns one child's compaction lineage,
// newest first.
func (r *Runtime) ListSessionAgentRunCompactions(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	params api.ListSessionAgentRunCompactionsParams,
) (*api.AgentRunCompactionPage, error) {
	limit, offset, err := pagingOptionsFromAPI(params.Limit, params.Offset)
	if err != nil {
		return nil, err
	}

	stored, err := r.store.ListAgentRunCompactions(
		ctx,
		sessionID,
		agentRunID,
		session.ListAgentRunCompactionsOptions{Limit: limit, Offset: offset},
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list durable child compactions")
	}

	converted, err := convertAgentRunPage(
		stored.Items,
		stored.Limit,
		stored.Offset,
		stored.HasMore,
		childCompactionPageKind,
		agentRunCompactionToAPI,
	)
	if err != nil {
		return nil, err
	}

	return &api.AgentRunCompactionPage{
		Compactions: converted.Items,
		HasMore:     converted.HasMore,
		Limit:       converted.Limit,
		Offset:      converted.Offset,
	}, nil
}

// agentRunPage is one converted child page's API envelope.
type agentRunPage[T any] struct {
	Items   []T
	Limit   int32
	Offset  int32
	HasMore bool
}

// convertAgentRunPage converts one stored child page into its API envelope,
// rejecting paging values the API cannot represent.
func convertAgentRunPage[S, T any](
	items []S,
	limit int,
	offset int,
	hasMore bool,
	kind string,
	convert func(S) (T, error),
) (agentRunPage[T], error) {
	pageLimit, err := messagePageValueToAPI(
		limit,
		fmt.Sprintf(childPageLimitField, kind),
	)
	if err != nil {
		return agentRunPage[T]{}, err
	}

	pageOffset, err := messagePageValueToAPI(
		offset,
		fmt.Sprintf(childPageOffsetField, kind),
	)
	if err != nil {
		return agentRunPage[T]{}, err
	}

	page := agentRunPage[T]{
		Items:   make([]T, 0, len(items)),
		Limit:   pageLimit,
		Offset:  pageOffset,
		HasMore: hasMore,
	}

	for _, item := range items {
		converted, convertErr := convert(item)
		if convertErr != nil {
			return agentRunPage[T]{}, convertErr
		}

		page.Items = append(page.Items, converted)
	}

	return page, nil
}

// GetSessionAgentRunCompaction reads one immutable child compaction owned by
// both the session and the agent run.
func (r *Runtime) GetSessionAgentRunCompaction(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	compactionID uuid.UUID,
) (*api.AgentRunCompaction, error) {
	stored, err := r.store.GetAgentRunCompaction(
		ctx,
		sessionID,
		agentRunID,
		compactionID,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get durable child compaction")
	}

	converted, err := agentRunCompactionToAPI(stored)
	if err != nil {
		return nil, err
	}

	return &converted, nil
}

func agentRunMessageToAPI(
	stored *models.AgentRunMessage,
) (api.AgentRunMessage, error) {
	if stored == nil {
		return api.AgentRunMessage{}, ctxerrors.Wrap(
			commerr.ErrInvalidState,
			"nil child message",
		)
	}

	message := api.AgentRunMessage{
		AgentRunId:   stored.AgentRunID,
		CompactionId: stored.CompactionID,
		Content:      stored.Content,
		CreatedAt:    stored.CreatedAt,
		Id:           stored.ID,
		Incomplete:   stored.Incomplete,
		IsError:      stored.IsError,
		Role:         api.AgentRunMessageRole(stored.Role),
		Sequence:     stored.Sequence,
		SessionId:    stored.SessionID,
	}

	if stored.ModelID != "" {
		message.Model = &stored.ModelID
	}

	if stored.Thinking != "" {
		message.Thinking = &stored.Thinking
	}

	if stored.ToolCallID != "" {
		message.ToolCallId = &stored.ToolCallID
	}

	if stored.ToolCallsJSON == "" {
		return message, nil
	}

	toolCalls := []map[string]any{}
	if err := json.Unmarshal(
		[]byte(stored.ToolCallsJSON),
		&toolCalls,
	); err != nil {
		return api.AgentRunMessage{}, ctxerrors.Wrap(
			err,
			"decode child message tool calls",
		)
	}

	message.ToolCalls = &toolCalls

	return message, nil
}

func agentRunCompactionToAPI(
	stored *models.AgentRunCompaction,
) (api.AgentRunCompaction, error) {
	if stored == nil {
		return api.AgentRunCompaction{}, ctxerrors.Wrap(
			commerr.ErrInvalidState,
			"nil child compaction",
		)
	}

	return api.AgentRunCompaction{
		AgentRunId:         stored.AgentRunID,
		CreatedAt:          stored.CreatedAt,
		DirectFromSequence: stored.DirectFromSequence,
		DirectToSequence:   stored.DirectToSequence,
		FromMessageId:      stored.FromMessageID,
		FromSequence:       stored.FromSequence,
		Id:                 stored.ID,
		InputTokenCount:    stored.InputTokenCount,
		Model:              stored.ModelID,
		ParentCompactionId: stored.SupersedesCompactionID,
		PromptHash:         stored.PromptHash,
		SessionId:          stored.SessionID,
		SourceMessageCount: stored.SourceMessageCount,
		Summary:            stored.Summary,
		SummaryTokenCount:  stored.SummaryTokenCount,
		ToMessageId:        stored.ToMessageID,
		ToSequence:         stored.ToSequence,
	}, nil
}
