package openaicompat

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/control"
)

func (h handler) readRequest(
	w http.ResponseWriter,
	r *http.Request,
) (completionRequest, int, *responseError) {
	var input completionRequest

	mediaType, _, err := mime.ParseMediaType(
		r.Header.Get(aichteeteapee.HeaderNameContentType),
	)
	if err != nil || mediaType != aichteeteapee.ContentTypeJSON {
		problem := invalidField("Content-Type")

		return input, http.StatusUnsupportedMediaType, &problem
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.dependencies.MaxRequestBytes)
	decoder := json.NewDecoder(r.Body)

	if err := decoder.Decode(&input); err != nil {
		status, problem := bodyError(err)

		return input, status, problem
	}

	var trailing json.RawMessage

	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		status, problem := bodyError(err)

		return input, status, problem
	}

	return input, 0, nil
}

func bodyError(err error) (int, *responseError) {
	var tooLarge *http.MaxBytesError

	if errors.As(err, &tooLarge) {
		return http.StatusRequestEntityTooLarge, &responseError{
			Message: messageBodyTooLarge,
			Type:    errorTypeRequest, Code: codeBodyTooLarge,
		}
	}

	problem := invalidField("body")

	return http.StatusBadRequest, &problem
}

func (h handler) mapRequest(
	r *http.Request,
	input completionRequest,
) (control.ConversationRequest, *responseError) {
	request, problem := h.selectSession(r)
	if problem != nil {
		return request, problem
	}

	if problem := h.validateOptions(input); problem != nil {
		return request, problem
	}

	message, problem := h.mapMessages(input.Messages, request.Fresh)
	if problem != nil {
		return request, problem
	}

	schema, problem := requestedSchema(input.ResponseFormat,
		h.dependencies.MaxOutputSchemaBytes)
	if problem != nil {
		return request, problem
	}

	message.OutputSchema = schema

	if input.Model != "" {
		message.Model = &input.Model
	} else if h.dependencies.DefaultModel != "" {
		model := h.dependencies.DefaultModel
		message.Model = &model
	}

	if input.ReasoningEffort != "" {
		message.ReasoningEffort = &input.ReasoningEffort
	}

	request.Message = message

	return request, nil
}

func (h handler) selectSession(
	r *http.Request,
) (control.ConversationRequest, *responseError) {
	request := control.ConversationRequest{}

	if len(r.Header.Values(headerSessionID)) > 1 ||
		len(r.Header.Values(headerWorkspace)) > 1 {
		problem := invalidField("session headers")

		return request, &problem
	}

	if value := r.Header.Get(headerSessionID); value != "" {
		sessionID, err := uuid.Parse(value)
		if err != nil || len(value) != uuidStringLength ||
			sessionID == uuid.Nil {
			problem := invalidField(headerSessionID)

			return request, &problem
		}

		request.SessionID = sessionID

		return request, nil
	}

	request.Fresh = true
	request.Workspace = r.Header.Get(headerWorkspace)

	if request.Workspace == "" {
		request.Workspace = h.dependencies.DefaultWorkspace
	}

	if strings.TrimSpace(request.Workspace) == "" {
		problem := invalidField(headerWorkspace)

		return request, &problem
	}

	return request, nil
}

func (h handler) validateOptions(input completionRequest) *responseError {
	for _, field := range []struct {
		name  string
		value json.RawMessage
	}{
		{name: "tools", value: input.Tools},
		{name: "tool_choice", value: input.ToolChoice},
		{name: "functions", value: input.Functions},
		{name: "logprobs", value: input.Logprobs},
	} {
		if len(field.value) != 0 && string(field.value) != "null" {
			problem := invalidField(field.name)

			return &problem
		}
	}

	if input.N != nil && *input.N != 1 {
		problem := invalidField("n")

		return &problem
	}

	if input.Model != "" && !h.hasModel(input.Model) {
		problem := invalidField("model")

		return &problem
	}

	return validateEffort(input.ReasoningEffort)
}

func (h handler) hasModel(name string) bool {
	for _, model := range h.dependencies.Models.ListModels().Models {
		if model.Name == name {
			return true
		}
	}

	return false
}

func validateEffort(effort string) *responseError {
	if effort == "" || slices.Contains([]string{
		elelem.ReasoningEffortMinimal,
		elelem.ReasoningEffortLow,
		elelem.ReasoningEffortMedium,
		elelem.ReasoningEffortHigh,
		elelem.ReasoningEffortXHigh,
		elelem.ReasoningEffortMax,
	}, effort) {
		return nil
	}

	problem := invalidField("reasoning_effort")

	return &problem
}

func requestedSchema(
	format *responseFormat,
	maximum int,
) (json.RawMessage, *responseError) {
	if format == nil || format.Type == formatText {
		return nil, nil
	}

	if format.Type == formatObject {
		return json.RawMessage(objectSchema), nil
	}

	if format.Type != formatSchema || format.JSONSchema == nil {
		problem := invalidField("response_format")

		return nil, &problem
	}

	schema := format.JSONSchema.Schema
	if err := agent.ValidateOutputSchema(schema, maximum); err != nil {
		problem := invalidField("response_format.json_schema.schema")

		return nil, &problem
	}

	return schema, nil
}
