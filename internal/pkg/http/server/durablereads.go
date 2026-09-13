package server

import (
	"context"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
)

// ListSessionTurns returns the durable turn lifecycle for one session.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListSessionTurns(
	ctx context.Context,
	request api.ListSessionTurnsRequestObject,
) (api.ListSessionTurnsResponseObject, error) {
	page, err := s.deps.Runtime.ListSessionTurns(ctx, request.Params)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return api.ListSessionTurns404JSONResponse{
				ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
					sessionNotFoundError(),
				),
			}, nil
		}

		if errors.Is(err, commerr.ErrValidationFailed) {
			return api.ListSessionTurns400JSONResponse{
				ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
					validationError(clientMessage(err)),
				),
			}, nil
		}

		return nil, ctxerrors.Wrap(err, "list session turns")
	}

	return api.ListSessionTurns200JSONResponse{
		Body: *page,
		Headers: api.ListSessionTurns200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// ListSessionCompactions returns every durable summary record for one session.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListSessionCompactions(
	ctx context.Context,
	request api.ListSessionCompactionsRequestObject,
) (api.ListSessionCompactionsResponseObject, error) {
	page, err := s.deps.Runtime.ListSessionCompactions(ctx, request.Params)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return api.ListSessionCompactions404JSONResponse{
				ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
					sessionNotFoundError(),
				),
			}, nil
		}

		if errors.Is(err, commerr.ErrValidationFailed) {
			return api.ListSessionCompactions400JSONResponse{
				ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
					validationError(clientMessage(err)),
				),
			}, nil
		}

		return nil, ctxerrors.Wrap(err, "list session compactions")
	}

	return api.ListSessionCompactions200JSONResponse{
		Body: *page,
		Headers: api.ListSessionCompactions200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// GetSessionCompaction returns one immutable compaction record for one session.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) GetSessionCompaction(
	ctx context.Context,
	request api.GetSessionCompactionRequestObject,
) (api.GetSessionCompactionResponseObject, error) {
	compaction, err := s.deps.Runtime.GetSessionCompaction(
		ctx,
		request.Params.XSessionID,
		request.CompactionId,
	)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return api.GetSessionCompaction404JSONResponse{
				ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
					notFoundError(),
				),
			}, nil
		}

		return nil, ctxerrors.Wrap(err, "get session compaction")
	}

	return api.GetSessionCompaction200JSONResponse{
		Body: *compaction,
		Headers: api.GetSessionCompaction200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// ListSessionModelRuns returns durable logical model invocations.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListSessionModelRuns(
	ctx context.Context,
	request api.ListSessionModelRunsRequestObject,
) (api.ListSessionModelRunsResponseObject, error) {
	page, err := s.deps.Runtime.ListSessionModelRuns(ctx, request.Params)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return api.ListSessionModelRuns404JSONResponse{
				ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
					sessionNotFoundError(),
				),
			}, nil
		}

		if errors.Is(err, commerr.ErrValidationFailed) {
			return api.ListSessionModelRuns400JSONResponse{
				ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
					validationError(clientMessage(err)),
				),
			}, nil
		}

		return nil, ctxerrors.Wrap(err, "list session model runs")
	}

	return api.ListSessionModelRuns200JSONResponse{
		Body: *page,
		Headers: api.ListSessionModelRuns200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// ListSessionModelRunCalls returns every provider round for one model run.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListSessionModelRunCalls(
	ctx context.Context,
	request api.ListSessionModelRunCallsRequestObject,
) (api.ListSessionModelRunCallsResponseObject, error) {
	page, err := s.deps.Runtime.ListSessionModelRunCalls(
		ctx,
		request.Params.XSessionID,
		request.ModelRunId,
		request.Params,
	)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return api.ListSessionModelRunCalls404JSONResponse{
				ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
					notFoundError(),
				),
			}, nil
		}

		if errors.Is(err, commerr.ErrValidationFailed) {
			return api.ListSessionModelRunCalls400JSONResponse{
				ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
					validationError(clientMessage(err)),
				),
			}, nil
		}

		return nil, ctxerrors.Wrap(err, "list session model run calls")
	}

	return api.ListSessionModelRunCalls200JSONResponse{
		Body: *page,
		Headers: api.ListSessionModelRunCalls200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// GetSessionContextSnapshot returns one session-referenced context snapshot.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) GetSessionContextSnapshot(
	ctx context.Context,
	request api.GetSessionContextSnapshotRequestObject,
) (api.GetSessionContextSnapshotResponseObject, error) {
	snapshot, err := s.deps.Runtime.GetSessionContextSnapshot(
		ctx,
		request.Params.XSessionID,
		request.ContextHash,
	)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return api.GetSessionContextSnapshot404JSONResponse{
				ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
					notFoundError(),
				),
			}, nil
		}

		return nil, ctxerrors.Wrap(err, "get session context snapshot")
	}

	return api.GetSessionContextSnapshot200JSONResponse{
		Body: *snapshot,
		Headers: api.GetSessionContextSnapshot200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// GetSessionPromptSnapshot returns one session-referenced prompt snapshot.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) GetSessionPromptSnapshot(
	ctx context.Context,
	request api.GetSessionPromptSnapshotRequestObject,
) (api.GetSessionPromptSnapshotResponseObject, error) {
	snapshot, err := s.deps.Runtime.GetSessionPromptSnapshot(
		ctx,
		request.Params.XSessionID,
		request.PromptHash,
	)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return api.GetSessionPromptSnapshot404JSONResponse{
				ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
					notFoundError(),
				),
			}, nil
		}

		return nil, ctxerrors.Wrap(err, "get session prompt snapshot")
	}

	return api.GetSessionPromptSnapshot200JSONResponse{
		Body: *snapshot,
		Headers: api.GetSessionPromptSnapshot200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// GetSessionAgentRun returns all persisted data for one child-agent run.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) GetSessionAgentRun(
	ctx context.Context,
	request api.GetSessionAgentRunRequestObject,
) (api.GetSessionAgentRunResponseObject, error) {
	run, err := s.deps.Runtime.GetSessionAgentRun(
		ctx,
		request.Params.XSessionID,
		request.AgentRunId,
	)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return api.GetSessionAgentRun404JSONResponse{
				ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
					notFoundError(),
				),
			}, nil
		}

		return nil, ctxerrors.Wrap(err, "get session agent run")
	}

	return api.GetSessionAgentRun200JSONResponse{
		Body: *run,
		Headers: api.GetSessionAgentRun200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}
