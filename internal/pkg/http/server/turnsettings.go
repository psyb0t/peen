package server

import (
	"encoding/json"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/http/wsapi"
)

func messageTurnSettings(
	data wsapi.MessageSendData,
) (agent.MessageRequest, error) {
	request := agent.MessageRequest{Message: data.Message, Model: data.Model}
	if data.To != nil &&
		(data.OutputSchema != nil || data.TimeoutSeconds != nil) {
		return request, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"turn settings cannot combine with to")
	}

	if data.OutputSchema != nil {
		encoded, err := json.Marshal(data.OutputSchema)
		if err != nil {
			return request, ctxerrors.Wrap(err, "encode output schema")
		}

		request.OutputSchema = encoded
	}

	if data.TimeoutSeconds != nil {
		request.TimeoutSeconds = int(*data.TimeoutSeconds)
	}

	return request, nil
}

func turnSettingsFailureFor(err error) (webSocketMessageFailure, bool) {
	switch {
	case errors.Is(err, agent.ErrOutputSchemaInvalid):
		return newWebSocketMessageFailure(ErrorCodeOutputSchemaInvalid,
			"output schema is invalid or exceeds its byte limit", ""), true
	case errors.Is(err, agent.ErrTurnTimeout):
		return newWebSocketMessageFailure(ErrorCodeTurnTimeout,
			"turn timeout elapsed", ""), true
	default:
		return webSocketMessageFailure{}, false
	}
}
