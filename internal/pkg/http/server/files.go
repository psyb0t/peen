package server

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/control"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
)

// ListWorkspaceFiles lists a workspace directory or returns file metadata.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListWorkspaceFiles(
	ctx context.Context,
	request api.ListWorkspaceFilesRequestObject,
) (api.ListWorkspaceFilesResponseObject, error) {
	requestedPath := ""
	if request.Params.Path != nil {
		requestedPath = *request.Params.Path
	}

	listing, err := s.deps.WorkspaceFiles.ListWorkspaceFiles(
		ctx,
		request.Params.XSessionID,
		requestedPath,
	)
	if err != nil {
		return s.listWorkspaceFilesFailure(ctx, request.Params.XSessionID, err)
	}

	return api.ListWorkspaceFiles200JSONResponse{
		Body: listing,
		Headers: api.ListWorkspaceFiles200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// ReadWorkspaceFile returns one bounded workspace file as base64.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ReadWorkspaceFile(
	ctx context.Context,
	request api.ReadWorkspaceFileRequestObject,
) (api.ReadWorkspaceFileResponseObject, error) {
	content, err := s.deps.WorkspaceFiles.ReadWorkspaceFile(
		ctx,
		request.Params.XSessionID,
		request.Params.Path,
	)
	if err != nil {
		return s.readWorkspaceFileFailure(ctx, request.Params.XSessionID, err)
	}

	return api.ReadWorkspaceFile200JSONResponse{
		Body: content,
		Headers: api.ReadWorkspaceFile200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// WriteWorkspaceFile atomically writes one workspace file.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) WriteWorkspaceFile(
	ctx context.Context,
	request api.WriteWorkspaceFileRequestObject,
) (api.WriteWorkspaceFileResponseObject, error) {
	if request.Body == nil {
		return api.WriteWorkspaceFile400JSONResponse{
			ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
				validationError("request body is required"),
			),
		}, nil
	}

	createDirectories := false
	if request.Body.CreateDirectories != nil {
		createDirectories = *request.Body.CreateDirectories
	}

	entry, err := s.deps.WorkspaceFiles.WriteWorkspaceFile(
		ctx,
		request.Params.XSessionID,
		request.Body.Path,
		request.Body.ContentBase64,
		createDirectories,
	)
	if err != nil {
		return s.writeWorkspaceFileFailure(ctx, request.Params.XSessionID, err)
	}

	return api.WriteWorkspaceFile200JSONResponse{
		Body: entry,
		Headers: api.WriteWorkspaceFile200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// DeleteWorkspaceFile removes one file, symlink, or empty directory.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) DeleteWorkspaceFile(
	ctx context.Context,
	request api.DeleteWorkspaceFileRequestObject,
) (api.DeleteWorkspaceFileResponseObject, error) {
	if err := s.deps.WorkspaceFiles.DeleteWorkspaceFile(
		ctx,
		request.Params.XSessionID,
		request.Params.Path,
	); err != nil {
		return s.deleteWorkspaceFileFailure(ctx, request.Params.XSessionID,
			err)
	}

	return api.DeleteWorkspaceFile204Response{
		Headers: api.DeleteWorkspaceFile204ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

//nolint:ireturn // Generated strict handler response interface.
func (s *Server) listWorkspaceFilesFailure(
	ctx context.Context,
	sessionID uuid.UUID,
	err error,
) (api.ListWorkspaceFilesResponseObject, error) {
	switch {
	case errors.Is(err, commerr.ErrPermissionDenied):
		return api.ListWorkspaceFiles403JSONResponse{
			ErrorForbiddenJSONResponse: api.ErrorForbiddenJSONResponse(
				workspaceFileRefusedError(err),
			),
		}, nil
	case errors.Is(err, commerr.ErrNotFound):
		body, lookupErr := s.missingRecordError(ctx, sessionID,
			workspacePathNotFoundMessage)
		if lookupErr != nil {
			return nil, lookupErr
		}

		return api.ListWorkspaceFiles404JSONResponse{
			ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(body),
		}, nil
	case errors.Is(err, commerr.ErrValidationFailed):
		return api.ListWorkspaceFiles400JSONResponse{
			ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
				validationError("workspace file request is invalid"),
			),
		}, nil
	default:
		return nil, ctxerrors.Wrap(err, "list workspace files")
	}
}

//nolint:ireturn // Generated strict handler response interface.
func (s *Server) readWorkspaceFileFailure(
	ctx context.Context,
	sessionID uuid.UUID,
	err error,
) (api.ReadWorkspaceFileResponseObject, error) {
	switch {
	case errors.Is(err, commerr.ErrPermissionDenied):
		return api.ReadWorkspaceFile403JSONResponse{
			ErrorForbiddenJSONResponse: api.ErrorForbiddenJSONResponse(
				workspaceFileRefusedError(err),
			),
		}, nil
	case errors.Is(err, commerr.ErrNotFound):
		body, lookupErr := s.missingRecordError(ctx, sessionID,
			workspacePathNotFoundMessage)
		if lookupErr != nil {
			return nil, lookupErr
		}

		return api.ReadWorkspaceFile404JSONResponse{
			ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(body),
		}, nil
	case errors.Is(err, commerr.ErrValidationFailed):
		return api.ReadWorkspaceFile400JSONResponse{
			ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
				validationError("workspace file request is invalid"),
			),
		}, nil
	default:
		return nil, ctxerrors.Wrap(err, "read workspace file")
	}
}

//nolint:ireturn // Generated strict handler response interface.
func (s *Server) writeWorkspaceFileFailure(
	ctx context.Context,
	sessionID uuid.UUID,
	err error,
) (api.WriteWorkspaceFileResponseObject, error) {
	switch {
	case errors.Is(err, commerr.ErrPermissionDenied):
		return api.WriteWorkspaceFile403JSONResponse{
			ErrorForbiddenJSONResponse: api.ErrorForbiddenJSONResponse(
				workspaceFileRefusedError(err),
			),
		}, nil
	case errors.Is(err, commerr.ErrNotFound):
		body, lookupErr := s.missingRecordError(ctx, sessionID,
			workspacePathNotFoundMessage)
		if lookupErr != nil {
			return nil, lookupErr
		}

		return api.WriteWorkspaceFile404JSONResponse{
			ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(body),
		}, nil
	case errors.Is(err, commerr.ErrValidationFailed):
		return api.WriteWorkspaceFile400JSONResponse{
			ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
				validationError("workspace file request is invalid"),
			),
		}, nil
	default:
		return nil, ctxerrors.Wrap(err, "write workspace file")
	}
}

//nolint:ireturn // Generated strict handler response interface.
func (s *Server) deleteWorkspaceFileFailure(
	ctx context.Context,
	sessionID uuid.UUID,
	err error,
) (api.DeleteWorkspaceFileResponseObject, error) {
	switch {
	case errors.Is(err, commerr.ErrPermissionDenied):
		return api.DeleteWorkspaceFile403JSONResponse{
			ErrorForbiddenJSONResponse: api.ErrorForbiddenJSONResponse(
				workspaceFileRefusedError(err),
			),
		}, nil
	case errors.Is(err, commerr.ErrNotFound):
		body, lookupErr := s.missingRecordError(ctx, sessionID,
			workspacePathNotFoundMessage)
		if lookupErr != nil {
			return nil, lookupErr
		}

		return api.DeleteWorkspaceFile404JSONResponse{
			ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(body),
		}, nil
	case errors.Is(err, commerr.ErrValidationFailed):
		return api.DeleteWorkspaceFile400JSONResponse{
			ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
				validationError("workspace file request is invalid"),
			),
		}, nil
	case errors.Is(err, commerr.ErrInvalidState):
		return api.DeleteWorkspaceFile409JSONResponse{
			ErrorConflictJSONResponse: api.ErrorConflictJSONResponse(
				workspaceFileConflictError(),
			),
		}, nil
	default:
		return nil, ctxerrors.Wrap(err, "delete workspace file")
	}
}

// workspaceFileRefusedError tells a refused file path apart from a refused
// session. A path refusal says what a path must look like; a session
// refusal keeps the workspace wording and names no directory.
func workspaceFileRefusedError(err error) api.Error {
	if !errors.Is(err, control.ErrWorkspacePathRefused) {
		return workspaceNotAllowedError()
	}

	return api.Error{
		Code:    ErrorCodeWorkspaceNotAllowed,
		Message: workspacePathRefusedMessage,
	}
}

func workspaceFileConflictError() api.Error {
	return api.Error{
		Code:    aichteeteapee.ErrorCodeConflict,
		Message: workspaceDirectoryNotEmptyMessage,
	}
}
