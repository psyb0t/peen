package server

import (
	"context"

	"github.com/psyb0t/peen/internal/pkg/http/api"
)

// ListWorkspaceRoots reports the configured workspace roots after normal API
// authentication, so the control surface can offer valid choices before a
// session exists.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListWorkspaceRoots(
	ctx context.Context,
	_ api.ListWorkspaceRootsRequestObject,
) (api.ListWorkspaceRootsResponseObject, error) {
	return api.ListWorkspaceRoots200JSONResponse{
		Body: s.deps.Sessions.WorkspaceRoots(),
		Headers: api.ListWorkspaceRoots200ResponseHeaders{
			XRequestID: requestID(ctx),
		},
	}, nil
}
