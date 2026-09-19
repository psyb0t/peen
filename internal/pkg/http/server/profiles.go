package server

import (
	"context"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// ListExecutionProfiles reports the profiles a client may name when it opens a
// workspace. It takes no session header because a client reads it before it
// has a session.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListExecutionProfiles(
	ctx context.Context,
	_ api.ListExecutionProfilesRequestObject,
) (api.ListExecutionProfilesResponseObject, error) {
	return api.ListExecutionProfiles200JSONResponse{
		Body: s.deps.Sessions.ExecutionProfiles(),
		Headers: api.ListExecutionProfiles200ResponseHeaders{
			XRequestID: requestID(ctx),
		},
	}, nil
}

// ListSessionWorkers reports one session's durable worker generations, so an
// operator can see which environment ran which turns.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListSessionWorkers(
	ctx context.Context,
	request api.ListSessionWorkersRequestObject,
) (api.ListSessionWorkersResponseObject, error) {
	page, err := s.deps.Sessions.ListWorkerGenerations(
		ctx,
		request.Params.XSessionID,
		api.ListSessionWorkersParams{
			Limit:  request.Params.Limit,
			Offset: request.Params.Offset,
		},
	)
	if err != nil {
		return listSessionWorkersFailure(err)
	}

	return api.ListSessionWorkers200JSONResponse{
		Body: page,
		Headers: api.ListSessionWorkers200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// ReconfigureSession moves one idle session to a different execution profile
// and records why. The client names only a profile: the operator decides what
// that name means.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ReconfigureSession(
	ctx context.Context,
	request api.ReconfigureSessionRequestObject,
) (api.ReconfigureSessionResponseObject, error) {
	if request.Body == nil {
		return api.ReconfigureSession400JSONResponse{
			ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
				validationError("a request body is required"),
			),
		}, nil
	}

	decision, err := s.deps.Sessions.ReconfigureSession(
		ctx,
		request.Params.XSessionID,
		*request.Body,
	)
	if err != nil {
		return reconfigureSessionFailure(err)
	}

	return api.ReconfigureSession200JSONResponse{
		Body: decision,
		Headers: api.ReconfigureSession200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// reconfigureSessionFailure maps a reconfiguration failure onto its response.
// An undefined profile is a refusal, and a session with a turn in flight is a
// conflict rather than a bad request: the same call succeeds once it is idle.
//
//nolint:ireturn // Generated strict handler response interface.
func reconfigureSessionFailure(
	err error,
) (api.ReconfigureSessionResponseObject, error) {
	if errors.Is(err, commerr.ErrPermissionDenied) {
		return api.ReconfigureSession403JSONResponse{
			ErrorForbiddenJSONResponse: api.ErrorForbiddenJSONResponse(
				executionProfileNotAllowedError(),
			),
		}, nil
	}

	if errors.Is(err, session.ErrSessionBusy) {
		return api.ReconfigureSession409JSONResponse{
			ErrorConflictJSONResponse: api.ErrorConflictJSONResponse(
				sessionBusyError(
					"a session with a running turn cannot be reconfigured",
				),
			),
		}, nil
	}

	if errors.Is(err, commerr.ErrNotFound) {
		return api.ReconfigureSession404JSONResponse{
			ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
				sessionNotFoundError(),
			),
		}, nil
	}

	if errors.Is(err, commerr.ErrValidationFailed) {
		return api.ReconfigureSession400JSONResponse{
			ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
				validationError(clientMessage(err)),
			),
		}, nil
	}

	return nil, ctxerrors.Wrap(err, "reconfigure session")
}

// ListSessionProfileDecisions reports one session's execution profile change
// history, so an operator can see who moved it and why.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListSessionProfileDecisions(
	ctx context.Context,
	request api.ListSessionProfileDecisionsRequestObject,
) (api.ListSessionProfileDecisionsResponseObject, error) {
	page, err := s.deps.Sessions.ListProfileDecisions(
		ctx,
		request.Params.XSessionID,
		api.ListSessionProfileDecisionsParams{
			Limit:  request.Params.Limit,
			Offset: request.Params.Offset,
		},
	)
	if err != nil {
		return listSessionProfileDecisionsFailure(err)
	}

	return api.ListSessionProfileDecisions200JSONResponse{
		Body: page,
		Headers: api.ListSessionProfileDecisions200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

//nolint:ireturn // Generated strict handler response interface.
func listSessionProfileDecisionsFailure(
	err error,
) (api.ListSessionProfileDecisionsResponseObject, error) {
	if errors.Is(err, commerr.ErrNotFound) {
		return api.ListSessionProfileDecisions404JSONResponse{
			ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
				sessionNotFoundError(),
			),
		}, nil
	}

	if errors.Is(err, commerr.ErrValidationFailed) {
		return api.ListSessionProfileDecisions400JSONResponse{
			ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
				validationError(clientMessage(err)),
			),
		}, nil
	}

	return nil, ctxerrors.Wrap(err, "list session profile decisions")
}

//nolint:ireturn // Generated strict handler response interface.
func listSessionWorkersFailure(
	err error,
) (api.ListSessionWorkersResponseObject, error) {
	if errors.Is(err, commerr.ErrNotFound) {
		return api.ListSessionWorkers404JSONResponse{
			ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
				sessionNotFoundError(),
			),
		}, nil
	}

	if errors.Is(err, commerr.ErrValidationFailed) {
		return api.ListSessionWorkers400JSONResponse{
			ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
				validationError(clientMessage(err)),
			),
		}, nil
	}

	return nil, ctxerrors.Wrap(err, "list session workers")
}
