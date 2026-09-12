package agent

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// ListSessionAgentRuns reports the child agents a session has launched, newest
// first, with the same bounded limit/offset paging as GET /v1/messages. A
// session that never launched one returns an empty list rather than an error.
func (r *Runtime) ListSessionAgentRuns(
	ctx context.Context,
	sessionID uuid.UUID,
	params api.ListSessionAgentRunsParams,
) (*api.AgentRunPage, error) {
	limit, offset, err := pagingOptionsFromAPI(params.Limit, params.Offset)
	if err != nil {
		return nil, err
	}

	var state *models.AgentRunState
	if params.State != nil {
		parsed := models.AgentRunState(*params.State)
		state = &parsed
	}

	stored, err := r.store.ListAgentRuns(ctx, sessionID, session.ListAgentRunsOptions{
		Limit:  limit,
		Offset: offset,
		State:  state,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list durable agent runs")
	}

	page := api.AgentRunPage{
		Agents:  make([]api.AgentRun, 0, len(stored.Items)),
		HasMore: stored.HasMore,
		Limit:   int32(stored.Limit),  //nolint:gosec // API validates this bound.
		Offset:  int32(stored.Offset), //nolint:gosec // API validates this bound.
	}
	for _, run := range stored.Items {
		converted, convertErr := agentRunModelToAPI(run)
		if convertErr != nil {
			return nil, convertErr
		}

		page.Agents = append(page.Agents, converted)
	}

	return &page, nil
}

// ListSessionAgentRunEvents follows one child through its complete SQLite
// replay log. The in-memory buffer never answers this read path.
func (r *Runtime) ListSessionAgentRunEvents(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	params api.ListSessionAgentRunEventsParams,
) (*api.AgentRunEventPage, error) {
	cursor := int64OrZero(params.Cursor)
	limit := int32OrZero(params.Limit)

	stored, err := r.store.ListAgentRunEvents(
		ctx,
		sessionID,
		agentRunID,
		session.ListAgentRunEventsOptions{
			Cursor: cursor,
			Limit:  limit,
		},
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list durable agent run events")
	}

	page := api.AgentRunEventPage{
		AgentRunId: agentRunID,
		State:      api.AgentRunEventPageState(stored.Run.State),
		Events:     make([]api.AgentRunEvent, 0, len(stored.Items)),
		NextCursor: stored.NextCursor,
		HasMore:    stored.HasMore,
	}

	for _, event := range stored.Items {
		converted, convertErr := agentRunEventToAPI(event)
		if convertErr != nil {
			return nil, convertErr
		}

		page.Events = append(page.Events, converted)
	}

	return &page, nil
}

// GetSessionAgentRun reads every durable detail of one child run.
func (r *Runtime) GetSessionAgentRun(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
) (*api.AgentRun, error) {
	stored, err := r.store.GetAgentRun(ctx, sessionID, agentRunID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get durable agent run")
	}

	converted, err := agentRunModelToAPI(stored)
	if err != nil {
		return nil, err
	}

	return &converted, nil
}

// CancelSessionAgentRun cancels one child agent without ending its parent
// turn. Cancelling an unknown, completed, or already cancelled run reports the
// current state rather than failing, matching POST /v1/session/cancel.
func (r *Runtime) CancelSessionAgentRun(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
) (*api.AgentRunCancelResponse, error) {
	run, requested, err := r.store.RequestAgentRunCancellation(
		ctx,
		sessionID,
		agentRunID,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "request durable agent run cancellation")
	}

	if requested {
		r.agentRunsMutex.Lock()
		registry := r.agentRuns[sessionID]
		r.agentRunsMutex.Unlock()
		if registry != nil {
			registry.Cancel(agentRunID)
		}
	}

	return &api.AgentRunCancelResponse{
		AgentRunId:      run.ID,
		CancelRequested: requested,
		State:           api.AgentRunCancelResponseState(run.State),
	}, nil
}

func agentRunModelToAPI(stored *models.AgentRun) (api.AgentRun, error) {
	if stored == nil {
		return api.AgentRun{}, ctxerrors.Wrap(commerr.ErrInvalidState, "nil agent run")
	}

	allowedTools := []any{}
	if err := json.Unmarshal([]byte(stored.AllowedToolsJSON), &allowedTools); err != nil {
		return api.AgentRun{}, ctxerrors.Wrap(err, "decode agent allowed tools")
	}
	responseMessages := []any{}
	if err := json.Unmarshal([]byte(stored.ResponseMessagesJSON), &responseMessages); err != nil {
		return api.AgentRun{}, ctxerrors.Wrap(err, "decode agent response messages")
	}

	run := api.AgentRun{
		AgentRunId:            stored.ID,
		AllowedTools:          allowedTools,
		CancelRequested:       stored.CancelRequested,
		CompletionTokenCount:  stored.CompletionTokenCount,
		Definition:            api.AgentRunDefinition(stored.Definition),
		Depth:                 stored.Depth,
		EndedAt:               stored.EndedAt,
		EventCount:            stored.EventCount,
		FailureClassification: stored.FailureClassification,
		FailureDetail:         stored.FailureDetail,
		FinishReason:          stored.FinishReason,
		Instructions:          stored.Instructions,
		Model:                 stored.ModelID,
		ModelReference:        stored.ModelReference,
		Name:                  stored.Name,
		ParentAgentRunId:      stored.ParentAgentRunID,
		ParentTurnId:          stored.ParentTurnID,
		PromptTokenCount:      stored.PromptTokenCount,
		RequestId:             stored.RequestID,
		ResponseMessages:      responseMessages,
		ResponseText:          stored.ResponseText,
		ResponseThinking:      stored.ResponseThinking,
		SessionId:             stored.SessionID,
		StartedAt:             stored.StartedAt,
		State:                 api.AgentRunState(stored.State),
		SystemPrompt:          stored.SystemPrompt,
		Task:                  stored.Task,
		Workspace:             stored.Workspace,
	}

	if stored.ParentToolCallID != "" {
		run.ParentToolCallId = &stored.ParentToolCallID
	}

	if stored.EndedAt != nil {
		ended := *stored.EndedAt
		run.EndedAt = &ended
	}

	return run, nil
}

func agentRunEventToAPI(event *models.AgentRunEvent) (api.AgentRunEvent, error) {
	converted := api.AgentRunEvent{
		Sequence:  event.Sequence,
		Type:      event.EventType,
		CreatedAt: event.CreatedAt,
	}

	if event.PayloadJSON == "" {
		return converted, nil
	}

	payload := map[string]any{}
	if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
		return api.AgentRunEvent{}, ctxerrors.Wrap(
			err,
			"decode agent run event payload",
		)
	}

	converted.Payload = &payload

	return converted, nil
}
