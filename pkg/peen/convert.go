package peen

import (
	"math"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/http/api"
)

// staticRegistry builds the internal model resolver from the caller's named
// Elelem upstream registry.
func staticRegistry(models map[string]ModelClient) (*agent.Registry, error) {
	converted := make(map[string]agent.ModelClient, len(models))
	for reference, client := range models {
		converted[reference] = agent.ModelClient{
			Client: client.Client,
			Model:  client.Model,
		}
	}

	registry, err := agent.NewStaticRegistry(converted)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build static model registry")
	}

	return registry, nil
}

// toTurnRequest converts one public message request into the internal
// runtime's transport-neutral turn input.
func toTurnRequest(request MessageRequest) (agent.TurnRequest, error) {
	input := agent.TurnRequest{
		Message:   request.Message,
		Workspace: request.Workspace,
	}

	if request.SessionID != "" {
		sessionID, err := parseSessionID(request.SessionID)
		if err != nil {
			return agent.TurnRequest{}, err
		}

		input.SessionID = &sessionID
	}

	if request.SystemPrompt != nil {
		input.SystemPrompt = request.SystemPrompt.Content
		input.SystemPromptMode = agent.PromptMode(request.SystemPrompt.Mode)
	}

	return input, nil
}

// toListMessagesParams converts one public page request into the internal
// runtime's API parameters, which already enforce the page bounds.
func toListMessagesParams(
	request ListMessagesRequest,
) (api.ListMessagesParams, error) {
	sessionID, err := parseSessionID(request.SessionID)
	if err != nil {
		return api.ListMessagesParams{}, err
	}

	params := api.ListMessagesParams{XSessionID: sessionID}

	if request.Limit > 0 {
		limit, err := toBoundedInt32(request.Limit, "limit")
		if err != nil {
			return api.ListMessagesParams{}, err
		}

		params.Limit = &limit
	}

	if request.Offset > 0 {
		offset, err := toBoundedInt32(request.Offset, "offset")
		if err != nil {
			return api.ListMessagesParams{}, err
		}

		params.Offset = &offset
	}

	if request.Order != "" {
		order, err := toAPIOrder(request.Order)
		if err != nil {
			return api.ListMessagesParams{}, err
		}

		params.Order = &order
	}

	return params, nil
}

// toBoundedInt32 rejects a page value that cannot cross the internal API
// boundary as int32, rather than silently wrapping it.
func toBoundedInt32(value int, field string) (int32, error) {
	if value < 0 || value > math.MaxInt32 {
		return 0, ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"invalid %s %d",
			field,
			value,
		)
	}

	return int32(value), nil
}

func toAPIOrder(order MessageOrder) (api.ListMessagesParamsOrder, error) {
	switch order {
	case MessageOrderAsc:
		return api.ListMessagesParamsOrderAsc, nil
	case MessageOrderDesc:
		return api.ListMessagesParamsOrderDesc, nil
	default:
		return "", ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"unknown message order %q",
			order,
		)
	}
}

func toListMessagesResult(page api.MessagePage) (ListMessagesResult, error) {
	items := make([]Message, 0, len(page.Items))

	for _, item := range page.Items {
		converted, err := toMessage(item)
		if err != nil {
			return ListMessagesResult{}, ctxerrors.Wrap(err, "convert message")
		}

		items = append(items, converted)
	}

	return ListMessagesResult{
		Items:   items,
		Limit:   int(page.Limit),
		Offset:  int(page.Offset),
		HasMore: page.HasMore,
	}, nil
}

func toMessage(item api.Message) (Message, error) {
	role, err := toMessageRole(item.Role)
	if err != nil {
		return Message{}, err
	}

	message := Message{
		ID:        item.Id.String(),
		Sequence:  item.Sequence,
		Role:      role,
		Content:   item.Content,
		CreatedAt: item.CreatedAt,
		Workspace: item.Workspace,
	}

	if item.Model != nil {
		message.Model = *item.Model
	}

	if item.Thinking != nil {
		message.Thinking = *item.Thinking
	}

	if item.ToolCallId != nil {
		message.ToolCallID = *item.ToolCallId
	}

	if item.IsError != nil {
		message.IsError = *item.IsError
	}

	if item.Incomplete != nil {
		message.Incomplete = *item.Incomplete
	}

	if item.ToolCalls != nil {
		message.ToolCalls = toMessageToolCalls(*item.ToolCalls)
	}

	return message, nil
}

func toMessageToolCalls(calls []api.MessageToolCall) []MessageToolCall {
	converted := make([]MessageToolCall, 0, len(calls))
	for _, call := range calls {
		converted = append(converted, MessageToolCall{
			ID:        call.Id,
			Name:      call.Name,
			Arguments: call.Arguments,
		})
	}

	return converted
}

func toMessageRole(role api.MessageRole) (MessageRole, error) {
	switch role {
	case api.MessageRoleUser:
		return MessageRoleUser, nil
	case api.MessageRoleAssistant:
		return MessageRoleAssistant, nil
	case api.MessageRoleTool:
		return MessageRoleTool, nil
	default:
		return "", ctxerrors.Wrapf(
			commerr.ErrInvalidState,
			"unknown message role %q",
			role,
		)
	}
}

func toSessionDetails(stored api.Session) SessionDetails {
	return SessionDetails{
		ID:                 stored.Id.String(),
		Agent:              stored.Agent,
		Model:              stored.Model,
		CreatedAt:          stored.CreatedAt,
		UpdatedAt:          stored.UpdatedAt,
		LastMessageAt:      stored.LastMessageAt,
		MessageCount:       stored.MessageCount,
		CompletedTurnCount: stored.CompletedTurnCount,
		ActiveTurn:         stored.ActiveTurn,
	}
}
