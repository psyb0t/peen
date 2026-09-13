package agent

import (
	"encoding/json"
	"math"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func sessionToAPI(
	stored *models.Session,
	activeTurn bool,
) api.Session {
	return api.Session{
		ActiveTurn:         activeTurn,
		Agent:              stored.RootAgent,
		CompletedTurnCount: stored.CompletedTurnCount,
		CreatedAt:          stored.CreatedAt,
		Id:                 stored.ID,
		LastMessageAt:      stored.LastMessageAt,
		MessageCount:       stored.MessageCount,
		Model:              stored.ModelID,
		UpdatedAt:          stored.UpdatedAt,
	}
}

func messagePageToAPI(page *session.MessagePage) (api.MessagePage, error) {
	limit, err := messagePageValueToAPI(page.Limit, "message page limit")
	if err != nil {
		return api.MessagePage{}, err
	}

	offset, err := messagePageValueToAPI(page.Offset, "message page offset")
	if err != nil {
		return api.MessagePage{}, err
	}

	items := make([]api.Message, 0, len(page.Items))
	for _, stored := range page.Items {
		message, err := messageToAPI(stored)
		if err != nil {
			return api.MessagePage{}, ctxerrors.Wrap(
				err,
				"convert stored message",
			)
		}

		items = append(items, message)
	}

	return api.MessagePage{
		HasMore: page.HasMore,
		Items:   items,
		Limit:   limit,
		Offset:  offset,
	}, nil
}

func messagePageValueToAPI(value int, field string) (int32, error) {
	if value < 0 || value > math.MaxInt32 {
		return 0, ctxerrors.Wrapf(
			commerr.ErrInvalidState,
			"invalid %s %d",
			field,
			value,
		)
	}

	return int32(value), nil
}

func messageToAPI(stored *models.Message) (api.Message, error) {
	if stored == nil {
		return api.Message{}, ctxerrors.Wrap(
			commerr.ErrInvalidState,
			"nil message",
		)
	}

	role, err := messageRoleToAPI(stored.Role)
	if err != nil {
		return api.Message{}, ctxerrors.Wrap(err, "convert message role")
	}

	toolCalls, err := toolCallsToAPI(stored.ToolCallsJSON)
	if err != nil {
		return api.Message{}, ctxerrors.Wrap(err, "convert stored tool calls")
	}

	message := api.Message{
		Content:   stored.Content,
		CreatedAt: stored.CreatedAt,
		Id:        stored.ID,
		Role:      role,
		Sequence:  stored.Sequence,
		Workspace: stored.Workspace,
	}
	applyMessageOptionalFields(&message, stored, toolCalls)

	return message, nil
}

func applyMessageOptionalFields(
	message *api.Message,
	stored *models.Message,
	toolCalls []api.MessageToolCall,
) {
	if stored.Incomplete {
		incomplete := true
		message.Incomplete = &incomplete
	}

	if stored.IsError {
		isError := true
		message.IsError = &isError
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

	if stored.CompactionID != nil {
		message.CompactionId = stored.CompactionID
	}

	if len(toolCalls) > 0 {
		message.ToolCalls = &toolCalls
	}
}

func messageRoleToAPI(role models.MessageRole) (api.MessageRole, error) {
	switch role {
	case models.MessageRoleUser:
		return api.MessageRoleUser, nil
	case models.MessageRoleAssistant:
		return api.MessageRoleAssistant, nil
	case models.MessageRoleTool:
		return api.MessageRoleTool, nil
	default:
		return "", ctxerrors.Wrapf(
			commerr.ErrInvalidState,
			"unknown message role %q",
			role,
		)
	}
}

func toolCallsToAPI(raw string) ([]api.MessageToolCall, error) {
	var toolCalls []api.MessageToolCall
	if err := json.Unmarshal([]byte(raw), &toolCalls); err != nil {
		return nil, ctxerrors.Wrap(err, "unmarshal tool calls")
	}

	return toolCalls, nil
}
