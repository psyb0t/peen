package server

import (
	"context"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
)

// ListSessionAgentRuns reports the child agents this session has launched.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListSessionAgentRuns(
	ctx context.Context,
	request api.ListSessionAgentRunsRequestObject,
) (api.ListSessionAgentRunsResponseObject, error) {
	page, err := s.deps.Runtime.ListSessionAgentRuns(
		ctx,
		request.Params.XSessionID,
		request.Params,
	)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return api.ListSessionAgentRuns404JSONResponse{
				ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
					notFoundError(),
				),
			}, nil
		}

		return nil, ctxerrors.Wrap(err, "list session agent runs")
	}

	return api.ListSessionAgentRuns200JSONResponse{
		Body: *page,
		Headers: api.ListSessionAgentRuns200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// ListSessionAgentRunEvents follows what one child agent is doing.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListSessionAgentRunEvents(
	ctx context.Context,
	request api.ListSessionAgentRunEventsRequestObject,
) (api.ListSessionAgentRunEventsResponseObject, error) {
	page, err := s.deps.Runtime.ListSessionAgentRunEvents(
		ctx,
		request.Params.XSessionID,
		request.AgentRunId,
		request.Params,
	)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return api.ListSessionAgentRunEvents404JSONResponse{
				ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
					notFoundError(),
				),
			}, nil
		}

		return nil, ctxerrors.Wrap(err, "list session agent run events")
	}

	return api.ListSessionAgentRunEvents200JSONResponse{
		Body: *page,
		Headers: api.ListSessionAgentRunEvents200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// CancelSessionAgentRun stops one child agent without ending its parent turn.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) CancelSessionAgentRun(
	ctx context.Context,
	request api.CancelSessionAgentRunRequestObject,
) (api.CancelSessionAgentRunResponseObject, error) {
	result, err := s.deps.Runtime.CancelSessionAgentRun(
		ctx,
		request.Params.XSessionID,
		request.AgentRunId,
	)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return api.CancelSessionAgentRun404JSONResponse{
				ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
					notFoundError(),
				),
			}, nil
		}

		return nil, ctxerrors.Wrap(err, "cancel session agent run")
	}

	return api.CancelSessionAgentRun202JSONResponse{
		Body: *result,
		Headers: api.CancelSessionAgentRun202ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}
