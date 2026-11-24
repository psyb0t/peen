package openaicompat

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/control"
)

// New expects paths with /openai/v1 stripped. Authentication and transport
// deadlines belong to the mounting server, before this handler reads a body.
func New(dependencies Dependencies) (http.Handler, error) {
	if dependencies.Runner == nil || dependencies.Models == nil ||
		dependencies.MaxRequestBytes <= 0 || dependencies.MaxImages < 0 ||
		dependencies.MaxImageBytes <= 0 ||
		dependencies.MaxOutputSchemaBytes < 0 {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"OpenAI adapter dependencies or limits are invalid")
	}

	return handler{dependencies: dependencies}, nil
}

func (h handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := h.serve(w, r); err != nil {
		ctxscope.GetLogger(r.Context()).Debug("OpenAI response write failed",
			"err", err)
	}
}

func (h handler) serve(w http.ResponseWriter, r *http.Request) error {
	switch r.URL.Path {
	case modelsPath:
		if r.Method != http.MethodGet {
			return methodError(w, http.MethodGet)
		}

		return h.listModels(w)
	case completionsPath:
		if r.Method != http.MethodPost {
			return methodError(w, http.MethodPost)
		}

		return h.complete(w, r)
	default:
		return writeJSON(w, http.StatusNotFound, errorEnvelope{
			Error: responseError{
				Message: messageNotFound,
				Type:    errorTypeRequest, Code: codeNotFound,
			},
		})
	}
}

func methodError(w http.ResponseWriter, allowed string) error {
	w.Header().Set(aichteeteapee.HeaderNameAllow, allowed)

	return writeJSON(w, http.StatusMethodNotAllowed, errorEnvelope{
		Error: responseError{
			Message: "Method not allowed.",
			Type:    errorTypeRequest, Code: codeMethodNotAllowed,
		},
	})
}

func (h handler) listModels(w http.ResponseWriter) error {
	result := modelList{Object: objectList, Data: []modelObject{}}
	for _, model := range h.dependencies.Models.ListModels().Models {
		result.Data = append(result.Data, modelObject{
			ID: model.Name, Object: objectModel, OwnedBy: model.ConnectionName,
		})
	}

	return writeJSON(w, http.StatusOK, result)
}

func (h handler) complete(w http.ResponseWriter, r *http.Request) error {
	input, status, problem := h.readRequest(w, r)
	if problem != nil {
		return writeJSON(w, status, errorEnvelope{Error: *problem})
	}

	request, problem := h.mapRequest(r, input)
	if problem != nil {
		return writeJSON(w, http.StatusBadRequest,
			errorEnvelope{Error: *problem})
	}

	stream := streamWriter{
		response: w,
		id:       completionIDPrefix + uuid.NewString(),
		model:    h.dependencies.DefaultModel, created: time.Now().Unix(),
	}
	if input.Model != "" {
		stream.model = input.Model
	}

	if input.Stream {
		request.OnEvent = stream.observe(r.Context(),
			len(request.Message.OutputSchema) != 0)
	}

	result, err := h.dependencies.Runner.Run(r.Context(), request)
	if err != nil {
		status, problem := runnerError(err)

		return stream.fail(status, problem)
	}

	return stream.answer(input.Stream, request, result)
}

// WriteUnauthorized gives the mounting authentication middleware the same
// error envelope as this compatibility handler.
func WriteUnauthorized(w http.ResponseWriter) error {
	return writeJSON(w, http.StatusUnauthorized, errorEnvelope{
		Error: responseError{
			Message: messageUnauthorized,
			Type:    errorTypeAuthentication, Code: codeUnauthorized,
		},
	})
}

// WriteInternalError hides transport setup details from clients.
func WriteInternalError(w http.ResponseWriter) error {
	return writeJSON(w, http.StatusInternalServerError, errorEnvelope{
		Error: responseError{
			Message: messageTurnFailed,
			Type:    errorTypeServer, Code: codeTurnFailed,
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ctxerrors.Wrap(err, "encode OpenAI response")
	}

	w.Header().Set(aichteeteapee.HeaderNameContentType,
		aichteeteapee.ContentTypeJSON)
	w.WriteHeader(status)

	if _, err := w.Write(encoded); err != nil {
		return ctxerrors.Wrap(err, "write OpenAI response")
	}

	return nil
}

func (s *streamWriter) answer(
	stream bool,
	request control.ConversationRequest,
	result *control.ConversationResult,
) error {
	text, problem := resultText(request, result)
	if problem != nil {
		return s.fail(http.StatusInternalServerError, *problem)
	}

	if !s.started {
		s.response.Header().Set(headerSessionID, result.SessionID.String())
	}

	if result.Model != "" {
		s.model = result.Model
	}

	if stream {
		return s.finish(text, result)
	}

	return writeJSON(s.response, http.StatusOK, completion{
		ID: s.id, Object: objectCompletion, Created: s.created, Model: s.model,
		Choices: []completionChoice{{
			Message:      completionMessage{Role: roleAssistant, Content: text},
			FinishReason: resultFinishReason(result),
		}},
		Usage: resultUsage(result),
	})
}
