package openaicompat

import (
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/http/api"
)

func resultText(
	request control.ConversationRequest,
	result *control.ConversationResult,
) (string, *responseError) {
	if result == nil || result.SessionID == uuid.Nil ||
		(result.State != api.TurnStateCompleted && !result.LengthLimited) {
		return "", &responseError{
			Message: messageTurnFailed,
			Type:    errorTypeServer, Code: codeTurnFailed,
		}
	}

	if len(request.Message.OutputSchema) == 0 {
		return result.Text, nil
	}

	if result.OutputError != "" || !json.Valid(result.Output) {
		return "", &responseError{
			Message: messageOutputInvalid,
			Type:    errorTypeServer, Code: codeOutputInvalid,
		}
	}

	return string(result.Output), nil
}

func resultFinishReason(result *control.ConversationResult) string {
	if result.LengthLimited {
		return finishLength
	}

	return finishStop
}

func resultUsage(result *control.ConversationResult) usage {
	return usage{
		PromptTokens:     result.Usage.PromptTokens,
		CompletionTokens: result.Usage.CompletionTokens,
		TotalTokens:      result.Usage.TotalTokens,
	}
}
