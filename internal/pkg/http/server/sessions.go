package server

import (
	"context"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/http/api"
)

// OpenSession resolves a workspace path to its durable session, creating that
// session the first time the directory is opened.
//
// This is the only operation that creates a session, which is what lets the
// control surface start with none.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) OpenSession(
	ctx context.Context,
	request api.OpenSessionRequestObject,
) (api.OpenSessionResponseObject, error) {
	if request.Body == nil {
		return api.OpenSession400JSONResponse{
			ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
				validationError("a request body is required"),
			),
		}, nil
	}

	profile := ""
	if request.Body.Profile != nil {
		profile = *request.Body.Profile
	}

	opened, err := s.deps.Sessions.OpenWorkspaceSession(
		ctx,
		request.Body.Workspace,
		profile,
	)
	if err != nil {
		return openSessionFailure(err)
	}

	return api.OpenSession200JSONResponse{
		Body: opened,
		Headers: api.OpenSession200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: opened.Session.Id,
		},
	}, nil
}

// openSessionFailure maps an open failure onto its response. A workspace the
// deployment does not expose is a 403 rather than a 404, so a client cannot
// probe which directories exist by reading the status code.
//
//nolint:ireturn // Generated strict handler response interface.
func openSessionFailure(
	err error,
) (api.OpenSessionResponseObject, error) {
	if errors.Is(err, commerr.ErrPermissionDenied) {
		return api.OpenSession403JSONResponse{
			ErrorForbiddenJSONResponse: api.ErrorForbiddenJSONResponse(
				workspaceNotAllowedError(),
			),
		}, nil
	}

	if errors.Is(err, commerr.ErrNotFound) {
		return api.OpenSession404JSONResponse{
			ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
				workspaceNotFoundError(),
			),
		}, nil
	}

	if errors.Is(err, commerr.ErrValidationFailed) {
		return api.OpenSession400JSONResponse{
			ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
				validationError(clientMessage(err)),
			),
		}, nil
	}

	return nil, ctxerrors.Wrap(err, "open workspace session")
}

// ListSessions reports the sessions this control surface holds, so a client
// can discover them without already knowing a session ID.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListSessions(
	ctx context.Context,
	request api.ListSessionsRequestObject,
) (api.ListSessionsResponseObject, error) {
	page, err := s.deps.Sessions.ListSessions(ctx, api.ListSessionsParams{
		Limit:  request.Params.Limit,
		Offset: request.Params.Offset,
	})
	if err != nil {
		if errors.Is(err, commerr.ErrValidationFailed) {
			return api.ListSessions400JSONResponse{
				ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
					validationError(clientMessage(err)),
				),
			}, nil
		}

		return nil, ctxerrors.Wrap(err, "list sessions")
	}

	return api.ListSessions200JSONResponse{
		Body: page,
		Headers: api.ListSessions200ResponseHeaders{
			XRequestID: requestID(ctx),
		},
	}, nil
}

// workspaceNotAllowedError names no root, because a refused caller is not
// entitled to learn the deployment's directory layout.
func workspaceNotAllowedError() api.Error {
	return api.Error{
		Code:    ErrorCodeWorkspaceNotAllowed,
		Message: "workspace is outside every configured workspace root",
	}
}

func workspaceNotFoundError() api.Error {
	return api.Error{
		Code:    ErrorCodeWorkspaceNotFound,
		Message: workspaceDirectoryNotFoundMessage,
	}
}
