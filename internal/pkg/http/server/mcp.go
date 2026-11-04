package server

import (
	"context"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/http/api"
)

// GetSessionMCPStatus reports the MCP servers the session's worker last
// reported.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) GetSessionMCPStatus(
	ctx context.Context,
	request api.GetSessionMCPStatusRequestObject,
) (api.GetSessionMCPStatusResponseObject, error) {
	report, err := s.deps.Sessions.MCPStatus(ctx, request.Params.XSessionID)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return api.GetSessionMCPStatus404JSONResponse{
				ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
					sessionNotFoundError(),
				),
			}, nil
		}

		return nil, ctxerrors.Wrap(err, "report session MCP status")
	}

	return api.GetSessionMCPStatus200JSONResponse{
		Body: report,
		Headers: api.GetSessionMCPStatus200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}
