package server

import (
	"context"

	"github.com/psyb0t/peen/internal/pkg/http/api"
)

// ListModels returns the model references the controller discovered at
// startup. It does not contact an upstream or expose connection credentials.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListModels(
	ctx context.Context,
	_ api.ListModelsRequestObject,
) (api.ListModelsResponseObject, error) {
	return api.ListModels200JSONResponse{
		Body: s.deps.Runtime.ListModels(),
		Headers: api.ListModels200ResponseHeaders{
			XRequestID: requestID(ctx),
		},
	}, nil
}
