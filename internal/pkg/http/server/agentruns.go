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

// ListSessionAgentRunMessages returns one child agent run's own transcript.
//
// A child agent runs a separate model context, so these rows are the child's
// and never the session's.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListSessionAgentRunMessages(
	ctx context.Context,
	request api.ListSessionAgentRunMessagesRequestObject,
) (api.ListSessionAgentRunMessagesResponseObject, error) {
	page, err := s.deps.Runtime.ListSessionAgentRunMessages(
		ctx,
		request.Params.XSessionID,
		request.AgentRunId,
		request.Params,
	)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return api.ListSessionAgentRunMessages404JSONResponse{
				ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
					notFoundError(),
				),
			}, nil
		}

		if errors.Is(err, commerr.ErrValidationFailed) {
			return api.ListSessionAgentRunMessages400JSONResponse{
				ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
					validationError(clientMessage(err)),
				),
			}, nil
		}

		return nil, ctxerrors.Wrap(err, "list session agent run messages")
	}

	return api.ListSessionAgentRunMessages200JSONResponse{
		Body: *page,
		Headers: api.ListSessionAgentRunMessages200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// ListSessionAgentRunCompactions returns one child's compaction lineage.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListSessionAgentRunCompactions(
	ctx context.Context,
	request api.ListSessionAgentRunCompactionsRequestObject,
) (api.ListSessionAgentRunCompactionsResponseObject, error) {
	page, err := s.deps.Runtime.ListSessionAgentRunCompactions(
		ctx,
		request.Params.XSessionID,
		request.AgentRunId,
		request.Params,
	)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return api.ListSessionAgentRunCompactions404JSONResponse{
				ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
					notFoundError(),
				),
			}, nil
		}

		if errors.Is(err, commerr.ErrValidationFailed) {
			return api.ListSessionAgentRunCompactions400JSONResponse{
				ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
					validationError(clientMessage(err)),
				),
			}, nil
		}

		return nil, ctxerrors.Wrap(err, "list session agent run compactions")
	}

	return api.ListSessionAgentRunCompactions200JSONResponse{
		Body: *page,
		Headers: api.ListSessionAgentRunCompactions200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// GetSessionAgentRunCompaction reads one immutable child compaction.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) GetSessionAgentRunCompaction(
	ctx context.Context,
	request api.GetSessionAgentRunCompactionRequestObject,
) (api.GetSessionAgentRunCompactionResponseObject, error) {
	compaction, err := s.deps.Runtime.GetSessionAgentRunCompaction(
		ctx,
		request.Params.XSessionID,
		request.AgentRunId,
		request.CompactionId,
	)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return api.GetSessionAgentRunCompaction404JSONResponse{
				ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
					notFoundError(),
				),
			}, nil
		}

		return nil, ctxerrors.Wrap(err, "get session agent run compaction")
	}

	return api.GetSessionAgentRunCompaction200JSONResponse{
		Body: *compaction,
		Headers: api.GetSessionAgentRunCompaction200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}
