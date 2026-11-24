package mcpserver

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (t tools) sessionID(ctx context.Context, value string) (uuid.UUID, error) {
	id, err := uuid.Parse(value)
	if err != nil || len(value) != uuidStringLength || id == uuid.Nil {
		return uuid.Nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"session ID must be a non-nil UUID")
	}

	if err := t.dependencies.Sessions.ValidateWorkspaceSession(ctx,
		id); err != nil {
		return uuid.Nil, ctxerrors.Wrap(err, "validate MCP workspace session")
	}

	return id, nil
}

func pageLimit(value *int32) (int32, error) {
	if value == nil {
		return session.DefaultPageLimit, nil
	}

	if *value < 1 || *value > session.MaximumPageLimit {
		return 0, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"MCP page limit is out of range")
	}

	return *value, nil
}

func (t tools) messageRequest(
	ctx context.Context,
	input sendInput,
) (control.ConversationRequest, error) {
	request := control.ConversationRequest{}
	if (input.Session == "") == (input.Workspace == "") ||
		strings.TrimSpace(input.Message) == "" ||
		(input.Session != "" && input.Space != "") {
		return request, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"message requires one session or workspace")
	}

	if input.Session != "" {
		id, err := t.sessionID(ctx, input.Session)
		if err != nil {
			return request, err
		}

		request.SessionID = id
	}

	if err := t.validateModelEffort(input.Model, input.Effort); err != nil {
		return request, err
	}

	message, err := t.turnSettings(input)
	if err != nil {
		return request, err
	}

	request.Workspace = input.Workspace
	request.AgentSpace = input.Space
	request.Message = message

	return request, nil
}

func (t tools) validateModelEffort(model, effort string) error {
	if model != "" && !t.hasModel(model) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"MCP model is not configured")
	}

	if effort != "" && !slices.Contains([]string{
		elelem.ReasoningEffortMinimal, elelem.ReasoningEffortLow,
		elelem.ReasoningEffortMedium, elelem.ReasoningEffortHigh,
		elelem.ReasoningEffortXHigh, elelem.ReasoningEffortMax,
	}, effort) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"MCP reasoning effort is invalid")
	}

	return nil
}

func (t tools) hasModel(name string) bool {
	for _, model := range t.dependencies.Reader.ListModels().Models {
		if model.Name == name {
			return true
		}
	}

	return false
}

func (t tools) turnSettings(input sendInput) (agent.MessageRequest, error) {
	request := agent.MessageRequest{Message: input.Message}
	if input.Timeout != nil {
		if *input.Timeout < 1 || *input.Timeout > agent.MaxTurnTimeoutSeconds {
			return request, ctxerrors.Wrap(commerr.ErrValidationFailed,
				"MCP turn timeout is out of range")
		}

		request.TimeoutSeconds = *input.Timeout
	}

	if input.Model != "" {
		request.Model = &input.Model
	}

	if input.Effort != "" {
		request.ReasoningEffort = &input.Effort
	}

	if input.Schema != nil {
		encoded, err := json.Marshal(input.Schema)
		if err != nil {
			return request, ctxerrors.Wrap(err, "encode MCP output schema")
		}

		if err := agent.ValidateOutputSchema(encoded,
			t.dependencies.MaxOutputSchemaBytes); err != nil {
			return request, ctxerrors.Wrap(err, "validate MCP output schema")
		}

		request.OutputSchema = encoded
	}

	return request, nil
}
