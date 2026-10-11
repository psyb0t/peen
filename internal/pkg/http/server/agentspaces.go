package server

import (
	"context"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// ListAgentSpaces reports the agent spaces a client may name when it opens a
// workspace. It takes no session header because a client reads it before it
// has a session.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListAgentSpaces(
	ctx context.Context,
	_ api.ListAgentSpacesRequestObject,
) (api.ListAgentSpacesResponseObject, error) {
	return api.ListAgentSpaces200JSONResponse{
		Body: s.deps.Sessions.AgentSpaces(),
		Headers: api.ListAgentSpaces200ResponseHeaders{
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

// ReconfigureSession moves one idle session to a different agent space and
// records why. The client names only an agent space: the operator decides what
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
// An agent space the deployment does not offer is a refusal, and a session with
// a turn in flight is a conflict rather than a bad request: the same call
// succeeds once it is idle.
//
//nolint:ireturn // Generated strict handler response interface.
func reconfigureSessionFailure(
	err error,
) (api.ReconfigureSessionResponseObject, error) {
	if errors.Is(err, commerr.ErrPermissionDenied) {
		return api.ReconfigureSession403JSONResponse{
			ErrorForbiddenJSONResponse: api.ErrorForbiddenJSONResponse(
				agentSpaceNotAllowedError(),
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

// ListSessionAgentSpaceDecisions reports one session's agent space change
// history, so an operator can see who moved it and why.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListSessionAgentSpaceDecisions(
	ctx context.Context,
	request api.ListSessionAgentSpaceDecisionsRequestObject,
) (api.ListSessionAgentSpaceDecisionsResponseObject, error) {
	page, err := s.deps.Sessions.ListAgentSpaceDecisions(
		ctx,
		request.Params.XSessionID,
		api.ListSessionAgentSpaceDecisionsParams{
			Limit:  request.Params.Limit,
			Offset: request.Params.Offset,
		},
	)
	if err != nil {
		return listSessionAgentSpaceDecisionsFailure(err)
	}

	return api.ListSessionAgentSpaceDecisions200JSONResponse{
		Body: page,
		Headers: api.ListSessionAgentSpaceDecisions200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

//nolint:ireturn // Generated strict handler response interface.
func listSessionAgentSpaceDecisionsFailure(
	err error,
) (api.ListSessionAgentSpaceDecisionsResponseObject, error) {
	if errors.Is(err, commerr.ErrNotFound) {
		return api.ListSessionAgentSpaceDecisions404JSONResponse{
			ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
				sessionNotFoundError(),
			),
		}, nil
	}

	if errors.Is(err, commerr.ErrValidationFailed) {
		return api.ListSessionAgentSpaceDecisions400JSONResponse{
			ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
				validationError(clientMessage(err)),
			),
		}, nil
	}

	return nil, ctxerrors.Wrap(err, "list session agent space decisions")
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
