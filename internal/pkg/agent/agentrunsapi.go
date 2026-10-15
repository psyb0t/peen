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
	options, err := listAgentRunsOptionsFromAPI(params)
	if err != nil {
		return nil, err
	}

	stored, err := r.store.ListAgentRuns(ctx, sessionID, options)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list durable agent runs")
	}

	questions, err := r.store.OpenAgentRunQuestions(ctx, sessionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list open agent run questions")
	}

	pageLimit, err := messagePageValueToAPI(
		stored.Limit,
		"agent run page limit",
	)
	if err != nil {
		return nil, err
	}

	pageOffset, err := messagePageValueToAPI(
		stored.Offset,
		"agent run page offset",
	)
	if err != nil {
		return nil, err
	}

	page := api.AgentRunPage{
		Agents:  make([]api.AgentRun, 0, len(stored.Items)),
		HasMore: stored.HasMore,
		Limit:   pageLimit,
		Offset:  pageOffset,
	}
	for _, run := range stored.Items {
		converted, convertErr := agentRunModelToAPI(run)
		if convertErr != nil {
			return nil, convertErr
		}

		page.Agents = append(
			page.Agents,
			withOpenQuestion(converted, questions),
		)
	}

	return &page, nil
}

// listAgentRunsOptionsFromAPI maps the list query. waiting is a state a
// caller filters by, though a waiting run is stored as running.
func listAgentRunsOptionsFromAPI(
	params api.ListSessionAgentRunsParams,
) (session.ListAgentRunsOptions, error) {
	limit, offset, err := pagingOptionsFromAPI(params.Limit, params.Offset)
	if err != nil {
		return session.ListAgentRunsOptions{}, err
	}

	options := session.ListAgentRunsOptions{Limit: limit, Offset: offset}

	if params.State != nil {
		state := models.AgentRunState(*params.State)
		if *params.State == api.ListSessionAgentRunsParamsStateWaiting {
			options.IsWaiting = true
			state = models.AgentRunStateRunning
		}

		options.State = &state
	}

	if params.ParentAgentRunId == nil {
		return options, nil
	}

	if *params.ParentAgentRunId == rootAgentRunReference {
		options.IsTopLevel = true

		return options, nil
	}

	parent, err := uuid.Parse(*params.ParentAgentRunId)
	if err != nil {
		return session.ListAgentRunsOptions{}, ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"parentAgentRunId must be an agent run ID or %q",
			rootAgentRunReference,
		)
	}

	options.ParentAgentRunID = &parent

	return options, nil
}

// withOpenQuestion marks a run waiting when it has a question nobody answered.
func withOpenQuestion(
	run api.AgentRun,
	questions map[uuid.UUID]string,
) api.AgentRun {
	question, isWaiting := questions[run.AgentRunId]
	if !isWaiting || run.State != api.AgentRunStateRunning {
		return run
	}

	run.Waiting = true
	run.Question = &question

	return run
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

	questions, err := r.store.OpenAgentRunQuestions(ctx, sessionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list open agent run questions")
	}

	converted = withOpenQuestion(converted, questions)

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
		return nil, ctxerrors.Wrap(
			err,
			"request durable agent run cancellation",
		)
	}

	if native := r.nativeWorkflowRunner(); native != nil &&
		run.State == models.AgentRunStateCancelled {
		if err := native.cancelChildTree(ctx, run); err != nil {
			return nil, ctxerrors.Wrap(err, "interrupt cancelled child work")
		}
	} else if requested {
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
		return api.AgentRun{}, ctxerrors.Wrap(
			commerr.ErrInvalidState,
			"nil agent run",
		)
	}

	payloads, err := agentRunPayloadsFromModel(stored)
	if err != nil {
		return api.AgentRun{}, err
	}

	run := api.AgentRun{
		AgentRunId:            stored.ID,
		AllowedTools:          payloads.allowedTools,
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
		ResponseMessages:      payloads.responseMessages,
		ResponseText:          stored.ResponseText,
		ResponseThinking:      stored.ResponseThinking,
		SessionId:             stored.SessionID,
		StartedAt:             stored.StartedAt,
		State:                 api.AgentRunState(stored.State),
		SystemPrompt:          stored.SystemPrompt,
		Task:                  stored.Task,
		Workspace:             stored.Workspace,
	}
	if stored.WorkerGenerationID != "" {
		run.WorkerGenerationId = &stored.WorkerGenerationID
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

type agentRunPayloads struct {
	allowedTools     []any
	responseMessages []any
}

func agentRunPayloadsFromModel(
	stored *models.AgentRun,
) (agentRunPayloads, error) {
	allowedTools := []any{}
	if err := json.Unmarshal(
		[]byte(stored.AllowedToolsJSON),
		&allowedTools,
	); err != nil {
		return agentRunPayloads{}, ctxerrors.Wrap(
			err,
			"decode agent allowed tools",
		)
	}

	responseMessages := []any{}
	if err := json.Unmarshal(
		[]byte(stored.ResponseMessagesJSON),
		&responseMessages,
	); err != nil {
		return agentRunPayloads{}, ctxerrors.Wrap(
			err,
			"decode agent response messages",
		)
	}

	return agentRunPayloads{
		allowedTools:     allowedTools,
		responseMessages: responseMessages,
	}, nil
}

func agentRunEventToAPI(
	event *models.AgentRunEvent,
) (api.AgentRunEvent, error) {
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
