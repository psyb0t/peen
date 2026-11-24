package server

import (
	"net/http"

	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/openaicompat"
)

func (s *Server) openAIHandler() http.Handler {
	if s.deps.OpenAIHandler == nil {
		return http.NotFoundHandler()
	}

	handler := http.StripPrefix(openAIBaseURL, s.deps.OpenAIHandler)

	return s.authenticate(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if err := prepareLongResponse(writer); err != nil {
			ctxscope.GetLogger(request.Context()).Error(
				"OpenAI response deadline failed", "err", err)

			writeErr := openaicompat.WriteInternalError(writer)
			if writeErr != nil {
				ctxscope.GetLogger(request.Context()).Debug(
					"OpenAI setup error response failed", "err", writeErr)
			}

			return
		}

		handler.ServeHTTP(writer, request)
	}))
}
