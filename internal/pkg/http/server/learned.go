package server

import (
	"context"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/http/api"
)

// ListSessionLearnedFiles lists the learned rules and skills the session
// loads, so a user can review what an auto-evolving agent saved.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListSessionLearnedFiles(
	ctx context.Context,
	request api.ListSessionLearnedFilesRequestObject,
) (api.ListSessionLearnedFilesResponseObject, error) {
	files, err := s.deps.Sessions.ListLearnedFiles(
		ctx,
		request.Params.XSessionID,
	)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return api.ListSessionLearnedFiles404JSONResponse{
				ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
					sessionNotFoundError(),
				),
			}, nil
		}

		return nil, ctxerrors.Wrap(err, "list session learned files")
	}

	return api.ListSessionLearnedFiles200JSONResponse{
		Body: files,
		Headers: api.ListSessionLearnedFiles200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// GetSessionLearnedFile returns one learned rule or skill.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) GetSessionLearnedFile(
	ctx context.Context,
	request api.GetSessionLearnedFileRequestObject,
) (api.GetSessionLearnedFileResponseObject, error) {
	content, err := s.deps.Sessions.ReadLearnedFile(
		ctx,
		request.Params.XSessionID,
		learnedFileReference(
			request.Params.Scope,
			request.Params.Kind,
			request.Params.Name,
		),
	)
	if err != nil {
		switch {
		case errors.Is(err, commerr.ErrNotFound):
			return api.GetSessionLearnedFile404JSONResponse{
				ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
					notFoundError(),
				),
			}, nil
		case errors.Is(err, commerr.ErrValidationFailed):
			return api.GetSessionLearnedFile400JSONResponse{
				ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
					validationError(clientMessage(err)),
				),
			}, nil
		default:
			return nil, ctxerrors.Wrap(err, "read session learned file")
		}
	}

	return api.GetSessionLearnedFile200JSONResponse{
		Body: content,
		Headers: api.GetSessionLearnedFile200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// DeleteSessionLearnedFile removes one learned rule or skill.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) DeleteSessionLearnedFile(
	ctx context.Context,
	request api.DeleteSessionLearnedFileRequestObject,
) (api.DeleteSessionLearnedFileResponseObject, error) {
	err := s.deps.Sessions.DeleteLearnedFile(
		ctx,
		request.Params.XSessionID,
		learnedFileReference(
			request.Params.Scope,
			request.Params.Kind,
			request.Params.Name,
		),
	)
	if err != nil {
		switch {
		case errors.Is(err, commerr.ErrNotFound):
			return api.DeleteSessionLearnedFile404JSONResponse{
				ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
					notFoundError(),
				),
			}, nil
		case errors.Is(err, commerr.ErrValidationFailed):
			return api.DeleteSessionLearnedFile400JSONResponse{
				ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
					validationError(clientMessage(err)),
				),
			}, nil
		default:
			return nil, ctxerrors.Wrap(err, "delete session learned file")
		}
	}

	return api.DeleteSessionLearnedFile204Response{
		Headers: api.DeleteSessionLearnedFile204ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

func learnedFileReference(
	scope api.LearnedScope,
	kind api.LearnedKind,
	name string,
) harness.LearnedFileReference {
	return harness.LearnedFileReference{
		Scope: harness.LearnedScope(scope),
		Kind:  harness.LearnedKind(kind),
		Name:  name,
	}
}
