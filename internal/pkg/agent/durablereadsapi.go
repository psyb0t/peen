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

// ListSessionEvents returns SQLite-backed protocol events, never an in-memory
// stream or queue view.
func (r *Runtime) ListSessionEvents(
	ctx context.Context,
	params api.ListSessionEventsParams,
) (*api.TranscriptEventPage, error) {
	options, err := listEventsOptionsFromAPI(params)
	if err != nil {
		return nil, err
	}

	stored, err := r.store.ListEvents(ctx, params.XSessionID, options)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list durable session events")
	}

	limit, err := messagePageValueToAPI(stored.Limit, "event page limit")
	if err != nil {
		return nil, err
	}

	offset, err := messagePageValueToAPI(stored.Offset, "event page offset")
	if err != nil {
		return nil, err
	}

	page := api.TranscriptEventPage{
		Events:  make([]api.TranscriptEvent, 0, len(stored.Items)),
		HasMore: stored.HasMore,
		Limit:   limit,
		Offset:  offset,
	}
	for _, event := range stored.Items {
		converted, convertErr := transcriptEventToAPI(event)
		if convertErr != nil {
			return nil, convertErr
		}

		page.Events = append(page.Events, converted)
	}

	return &page, nil
}

// ListSessionTurns returns the complete durable turn lifecycle.
func (r *Runtime) ListSessionTurns(
	ctx context.Context,
	params api.ListSessionTurnsParams,
) (*api.TurnPage, error) {
	limit, offset, err := pagingOptionsFromAPI(params.Limit, params.Offset)
	if err != nil {
		return nil, err
	}

	stored, err := r.store.ListTurns(
		ctx,
		params.XSessionID,
		session.ListTurnsOptions{
			Limit:  limit,
			Offset: offset,
		},
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list durable session turns")
	}

	pageLimit, pageOffset, err := durablePageValues(
		stored.Limit,
		stored.Offset,
		"turn",
	)
	if err != nil {
		return nil, err
	}

	page := api.TurnPage{
		Turns:   mapStoredItems(stored.Items, turnToAPI),
		HasMore: stored.HasMore,
		Limit:   pageLimit,
		Offset:  pageOffset,
	}

	return &page, nil
}

// ListSessionCompactions returns every stored summary record for replay.
func (r *Runtime) ListSessionCompactions(
	ctx context.Context,
	params api.ListSessionCompactionsParams,
) (*api.CompactionPage, error) {
	limit, offset, err := pagingOptionsFromAPI(params.Limit, params.Offset)
	if err != nil {
		return nil, err
	}

	stored, err := r.store.ListCompactions(
		ctx,
		params.XSessionID,
		session.ListCompactionsOptions{Limit: limit, Offset: offset},
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list durable session compactions")
	}

	pageLimit, pageOffset, err := durablePageValues(
		stored.Limit,
		stored.Offset,
		"compaction",
	)
	if err != nil {
		return nil, err
	}

	page := api.CompactionPage{
		Compactions: mapStoredItems(stored.Items, compactionToAPI),
		HasMore:     stored.HasMore,
		Limit:       pageLimit,
		Offset:      pageOffset,
	}

	return &page, nil
}

// GetSessionCompaction reads one immutable summary record and its parent link.
func (r *Runtime) GetSessionCompaction(
	ctx context.Context,
	sessionID uuid.UUID,
	compactionID uuid.UUID,
) (*api.Compaction, error) {
	stored, err := r.store.GetCompaction(ctx, sessionID, compactionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get durable session compaction")
	}

	converted := compactionToAPI(stored)

	return &converted, nil
}

// ListSessionModelRuns returns every durable model invocation in a session.
func (r *Runtime) ListSessionModelRuns(
	ctx context.Context,
	params api.ListSessionModelRunsParams,
) (*api.ModelRunPage, error) {
	options, err := modelRunListOptionsFromAPI(params)
	if err != nil {
		return nil, err
	}

	stored, err := r.store.ListModelRuns(ctx, params.XSessionID, options)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list durable model runs")
	}

	pageLimit, pageOffset, err := durablePageValues(
		stored.Limit,
		stored.Offset,
		"model run",
	)
	if err != nil {
		return nil, err
	}

	page := api.ModelRunPage{
		HasMore:   stored.HasMore,
		Limit:     pageLimit,
		ModelRuns: make([]api.ModelRun, 0, len(stored.Items)),
		Offset:    pageOffset,
	}
	for _, item := range stored.Items {
		converted, convertErr := modelRunToAPI(item)
		if convertErr != nil {
			return nil, convertErr
		}

		page.ModelRuns = append(page.ModelRuns, converted)
	}

	return &page, nil
}

// ListSessionModelRunCalls returns each durable provider request and response
// that belongs to one logical model invocation.
func (r *Runtime) ListSessionModelRunCalls(
	ctx context.Context,
	sessionID uuid.UUID,
	modelRunID uuid.UUID,
	params api.ListSessionModelRunCallsParams,
) (*api.ModelCallPage, error) {
	limit, offset, err := pagingOptionsFromAPI(params.Limit, params.Offset)
	if err != nil {
		return nil, err
	}

	stored, err := r.store.ListModelCalls(
		ctx,
		sessionID,
		modelRunID,
		session.ListModelCallsOptions{Limit: limit, Offset: offset},
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list durable model calls")
	}

	run, err := modelRunToAPI(stored.Run)
	if err != nil {
		return nil, err
	}

	pageLimit, pageOffset, err := durablePageValues(
		stored.Limit,
		stored.Offset,
		"model call",
	)
	if err != nil {
		return nil, err
	}

	page := api.ModelCallPage{
		Calls:    make([]api.ModelCall, 0, len(stored.Items)),
		HasMore:  stored.HasMore,
		Limit:    pageLimit,
		ModelRun: run,
		Offset:   pageOffset,
	}
	for _, item := range stored.Items {
		converted, convertErr := modelCallToAPI(item)
		if convertErr != nil {
			return nil, convertErr
		}

		page.Calls = append(page.Calls, converted)
	}

	return &page, nil
}

// GetSessionContextSnapshot reads the exact resolved context used by this
// session.
func (r *Runtime) GetSessionContextSnapshot(
	ctx context.Context,
	sessionID uuid.UUID,
	hash string,
) (*api.ContextSnapshot, error) {
	stored, err := r.store.GetContextSnapshot(ctx, sessionID, hash)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get durable context snapshot")
	}

	manifest := map[string]any{}
	if err := json.Unmarshal(
		[]byte(stored.ManifestJSON),
		&manifest,
	); err != nil {
		return nil, ctxerrors.Wrap(
			err,
			"decode context snapshot manifest",
		)
	}

	return &api.ContextSnapshot{
		CreatedAt:       stored.CreatedAt,
		Hash:            stored.Hash,
		Manifest:        manifest,
		ResolvedContent: stored.ResolvedContent,
	}, nil
}

// GetSessionPromptSnapshot reads the exact effective prompt used by this
// session.
func (r *Runtime) GetSessionPromptSnapshot(
	ctx context.Context,
	sessionID uuid.UUID,
	hash string,
) (*api.PromptSnapshot, error) {
	stored, err := r.store.GetPromptSnapshot(ctx, sessionID, hash)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get durable prompt snapshot")
	}

	return &api.PromptSnapshot{
		CreatedAt:       stored.CreatedAt,
		EffectivePrompt: stored.EffectivePrompt,
		Hash:            stored.Hash,
	}, nil
}

func listEventsOptionsFromAPI(
	params api.ListSessionEventsParams,
) (session.ListEventsOptions, error) {
	limit, offset, err := pagingOptionsFromAPI(params.Limit, params.Offset)
	if err != nil {
		return session.ListEventsOptions{}, err
	}

	order := session.PageOrderAscending

	if params.Order != nil {
		switch *params.Order {
		case api.ListSessionEventsParamsOrderAsc:
			order = session.PageOrderAscending
		case api.ListSessionEventsParamsOrderDesc:
			order = session.PageOrderDescending
		default:
			return session.ListEventsOptions{}, ctxerrors.Wrap(
				commerr.ErrValidationFailed,
				"event order",
			)
		}
	}

	return session.ListEventsOptions{
		Limit:  limit,
		Offset: offset,
		Order:  order,
	}, nil
}

func durablePageValues(
	limit int,
	offset int,
	resource string,
) (int32, int32, error) {
	pageLimit, err := messagePageValueToAPI(limit, resource+" page limit")
	if err != nil {
		return 0, 0, err
	}

	pageOffset, err := messagePageValueToAPI(offset, resource+" page offset")
	if err != nil {
		return 0, 0, err
	}

	return pageLimit, pageOffset, nil
}

func mapStoredItems[T any, U any](
	items []*T,
	convert func(*T) U,
) []U {
	converted := make([]U, 0, len(items))
	for _, item := range items {
		converted = append(converted, convert(item))
	}

	return converted
}

func modelRunListOptionsFromAPI(
	params api.ListSessionModelRunsParams,
) (session.ListModelRunsOptions, error) {
	limit, offset, err := pagingOptionsFromAPI(params.Limit, params.Offset)
	if err != nil {
		return session.ListModelRunsOptions{}, err
	}

	options := session.ListModelRunsOptions{Limit: limit, Offset: offset}

	if params.Stage != nil {
		stage := models.ModelRunStage(*params.Stage)
		options.Stage = &stage
	}

	if params.State != nil {
		state := models.ModelRunState(*params.State)
		options.State = &state
	}

	return options, nil
}

func turnToAPI(stored *models.Turn) api.Turn {
	return api.Turn{
		CancelRequested:       stored.CancelRequested,
		CompletedAt:           stored.CompletedAt,
		ContextSnapshotHash:   stored.ContextSnapshotHash,
		FailureClassification: stored.FailureClassification,
		Id:                    stored.ID,
		PromptSnapshotHash:    stored.PromptSnapshotHash,
		RequestId:             stored.RequestID,
		SessionId:             stored.SessionID,
		StartedAt:             stored.StartedAt,
		State:                 api.TurnState(stored.State),
		Workspace:             stored.Workspace,
	}
}

func compactionToAPI(stored *models.Compaction) api.Compaction {
	return api.Compaction{
		CreatedAt:          stored.CreatedAt,
		FromMessageId:      stored.FromMessageID,
		FromSequence:       stored.FromSequence,
		Id:                 stored.ID,
		InputTokenCount:    stored.InputTokenCount,
		Model:              stored.ModelID,
		PromptHash:         stored.PromptHash,
		SessionId:          stored.SessionID,
		SourceMessageCount: stored.SourceMessageCount,
		Summary:            stored.Summary,
		SummaryTokenCount:  stored.SummaryTokenCount,
		ParentCompactionId: stored.SupersedesCompactionID,
		ToMessageId:        stored.ToMessageID,
		ToSequence:         stored.ToSequence,
	}
}

func modelRunToAPI(stored *models.ModelRun) (api.ModelRun, error) {
	if stored == nil {
		return api.ModelRun{}, ctxerrors.Wrap(
			commerr.ErrInvalidState,
			"nil model run",
		)
	}

	payloads, err := decodeModelRunPayloads(stored)
	if err != nil {
		return api.ModelRun{}, err
	}

	return api.ModelRun{
		AgentRunId:            stored.AgentRunID,
		BilledCostAmount:      stored.BilledCostAmount,
		CompletedAt:           stored.CompletedAt,
		ConnectionName:        stored.ConnectionName,
		CostKnown:             stored.CostKnown,
		FailureClassification: stored.FailureClassification,
		FailureDetail:         stored.FailureDetail,
		FinishReason:          stored.FinishReason,
		Id:                    stored.ID,
		ModelReference:        stored.ModelReference,
		RequestSettings:       payloads.requestSettings,
		RequestedModelId:      stored.RequestedModelID,
		ResponseCostAmount:    stored.ResponseCostAmount,
		ResponseInjections:    payloads.responseInjections,
		ResponseMessages:      payloads.responseMessages,
		ResponseModelId:       stored.ResponseModelID,
		ResponseText:          stored.ResponseText,
		ResponseThinking:      stored.ResponseThinking,
		ResponseUsage:         payloads.responseUsage,
		RetryCostAmount:       stored.RetryCostAmount,
		SessionId:             stored.SessionID,
		Stage:                 api.ModelRunStage(stored.Stage),
		StartedAt:             stored.StartedAt,
		State:                 api.ModelRunState(stored.State),
		TurnId:                stored.TurnID,
	}, nil
}

type modelRunPayloads struct {
	requestSettings    map[string]any
	responseMessages   []any
	responseInjections []any
	responseUsage      map[string]any
}

func decodeModelRunPayloads(stored *models.ModelRun) (modelRunPayloads, error) {
	requestSettings, err := decodeJSONObject(
		stored.RequestSettingsJSON,
		"model run request settings",
	)
	if err != nil {
		return modelRunPayloads{}, err
	}

	responseMessages, err := decodeJSONArray(
		stored.ResponseMessagesJSON,
		"model run response messages",
	)
	if err != nil {
		return modelRunPayloads{}, err
	}

	responseInjections, err := decodeJSONArray(
		stored.ResponseInjectionsJSON,
		"model run response injections",
	)
	if err != nil {
		return modelRunPayloads{}, err
	}

	responseUsage, err := decodeJSONObject(
		stored.ResponseUsageJSON,
		"model run response usage",
	)
	if err != nil {
		return modelRunPayloads{}, err
	}

	return modelRunPayloads{
		requestSettings:    requestSettings,
		responseMessages:   responseMessages,
		responseInjections: responseInjections,
		responseUsage:      responseUsage,
	}, nil
}

func modelCallToAPI(stored *models.ModelCall) (api.ModelCall, error) {
	if stored == nil {
		return api.ModelCall{}, ctxerrors.Wrap(
			commerr.ErrInvalidState,
			"nil model call",
		)
	}

	payloads, err := decodeModelCallPayloads(stored)
	if err != nil {
		return api.ModelCall{}, err
	}

	return api.ModelCall{
		BilledCostAmount:        stored.BilledCostAmount,
		CacheReadTokens:         stored.CacheReadTokens,
		CacheWriteLongTtlTokens: stored.CacheWriteLongTTLTokens,
		CacheWriteTokens:        stored.CacheWriteTokens,
		CompletedAt:             stored.CompletedAt,
		CompletionTokens:        stored.CompletionTokens,
		CostKnown:               stored.CostKnown,
		FailureClassification:   stored.FailureClassification,
		FailureDetail:           stored.FailureDetail,
		FinishReason:            stored.FinishReason,
		Id:                      stored.ID,
		ModelRunId:              stored.ModelRunID,
		PromptTokens:            stored.PromptTokens,
		ReasoningTokens:         stored.ReasoningTokens,
		RequestMessages:         payloads.requestMessages,
		RequestTools:            payloads.requestTools,
		ResponseCostAmount:      stored.ResponseCostAmount,
		ResponseMessage:         payloads.responseMessage,
		ResponseModelId:         stored.ResponseModelID,
		ResponseUsage:           payloads.responseUsage,
		RetryAttemptCount:       stored.RetryAttemptCount,
		RetryAttempts:           payloads.retryAttempts,
		RetryCostAmount:         stored.RetryCostAmount,
		Round:                   stored.Round,
		SessionId:               stored.SessionID,
		StartedAt:               stored.StartedAt,
		State:                   api.ModelCallState(stored.State),
		TotalAttempts:           stored.TotalAttempts,
		TotalTokens:             stored.TotalTokens,
		WastedCompletionTokens:  stored.WastedCompletionTokens,
		WastedPromptTokens:      stored.WastedPromptTokens,
		WastedTotalTokens:       stored.WastedTotalTokens,
	}, nil
}

type modelCallPayloads struct {
	requestMessages []any
	requestTools    []any
	responseMessage *map[string]any
	responseUsage   map[string]any
	retryAttempts   []map[string]any
}

func decodeModelCallPayloads(
	stored *models.ModelCall,
) (modelCallPayloads, error) {
	requestMessages, err := decodeJSONArray(
		stored.RequestMessagesJSON,
		"model call request messages",
	)
	if err != nil {
		return modelCallPayloads{}, err
	}

	requestTools, err := decodeJSONArray(
		stored.RequestToolsJSON,
		"model call request tools",
	)
	if err != nil {
		return modelCallPayloads{}, err
	}

	responseMessage, hasResponseMessage, err := decodeNullableJSONObject(
		stored.ResponseMessageJSON,
		"model call response message",
	)
	if err != nil {
		return modelCallPayloads{}, err
	}

	var responseMessagePointer *map[string]any
	if hasResponseMessage {
		responseMessagePointer = &responseMessage
	}

	responseUsage, err := decodeJSONObject(
		stored.ResponseUsageJSON,
		"model call response usage",
	)
	if err != nil {
		return modelCallPayloads{}, err
	}

	retryAttempts, err := decodeJSONObjectArray(
		stored.RetryAttemptsJSON,
		"model call retry attempts",
	)
	if err != nil {
		return modelCallPayloads{}, err
	}

	return modelCallPayloads{
		requestMessages: requestMessages,
		requestTools:    requestTools,
		responseMessage: responseMessagePointer,
		responseUsage:   responseUsage,
		retryAttempts:   retryAttempts,
	}, nil
}

func decodeJSONObject(raw string, field string) (map[string]any, error) {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, ctxerrors.Wrapf(err, "decode %s", field)
	}

	if decoded == nil {
		return nil, ctxerrors.Wrapf(
			commerr.ErrInvalidState,
			"%s is null",
			field,
		)
	}

	return decoded, nil
}

func decodeNullableJSONObject(
	raw string,
	field string,
) (map[string]any, bool, error) {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, false, ctxerrors.Wrapf(err, "decode %s", field)
	}

	if decoded == nil {
		return nil, false, nil
	}

	return decoded, true, nil
}

func decodeJSONArray(raw string, field string) ([]any, error) {
	var decoded []any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, ctxerrors.Wrapf(err, "decode %s", field)
	}

	if decoded == nil {
		return []any{}, nil
	}

	return decoded, nil
}

func decodeJSONObjectArray(raw string, field string) ([]map[string]any, error) {
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, ctxerrors.Wrapf(err, "decode %s", field)
	}

	if decoded == nil {
		return []map[string]any{}, nil
	}

	return decoded, nil
}

func transcriptEventToAPI(stored *models.Event) (api.TranscriptEvent, error) {
	payload := map[string]any{}
	if err := json.Unmarshal([]byte(stored.PayloadJSON), &payload); err != nil {
		return api.TranscriptEvent{}, ctxerrors.Wrap(
			err,
			"decode durable event payload",
		)
	}

	converted := api.TranscriptEvent{
		CreatedAt: stored.CreatedAt,
		Id:        stored.ID,
		Payload:   payload,
		RequestId: stored.RequestID,
		Sequence:  stored.Sequence,
		SessionId: stored.SessionID,
		TurnId:    stored.TurnID,
		Type:      stored.EventType,
	}
	if stored.ParentToolCallID != "" {
		converted.ParentToolCallId = &stored.ParentToolCallID
	}

	return converted, nil
}
