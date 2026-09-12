package server

import (
	"context"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/events"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
)

// ListMessages returns one stable bounded durable transcript page.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListMessages(
	ctx context.Context,
	request api.ListMessagesRequestObject,
) (api.ListMessagesResponseObject, error) {
	page, err := s.deps.Runtime.ListMessages(ctx, request.Params)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return listMessagesNotFound(), nil
		}

		if errors.Is(err, commerr.ErrValidationFailed) {
			return listMessagesBadRequest(clientMessage(err)), nil
		}

		return nil, ctxerrors.Wrap(err, "list session messages")
	}

	return api.ListMessages200JSONResponse{
		Body: *page,
		Headers: api.ListMessages200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// GetSession returns durable metadata and active-turn state for one session.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) GetSession(
	ctx context.Context,
	request api.GetSessionRequestObject,
) (api.GetSessionResponseObject, error) {
	details, err := s.deps.Runtime.Session(ctx, request.Params.XSessionID)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return getSessionNotFound(), nil
		}

		return nil, ctxerrors.Wrap(err, "get session")
	}

	return api.GetSession200JSONResponse{
		Body: *details,
		Headers: api.GetSession200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// CancelSession asks an active agent turn to stop without deleting its session.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) CancelSession(
	ctx context.Context,
	request api.CancelSessionRequestObject,
) (api.CancelSessionResponseObject, error) {
	result, err := s.deps.Runtime.CancelSession(ctx, request.Params.XSessionID)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return cancelSessionNotFound(), nil
		}

		return nil, ctxerrors.Wrap(err, "cancel session")
	}

	return api.CancelSession202JSONResponse{
		Body: *result,
		Headers: api.CancelSession202ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// ListSessionEvents returns durable protocol history without consuming it.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListSessionEvents(
	ctx context.Context,
	request api.ListSessionEventsRequestObject,
) (api.ListSessionEventsResponseObject, error) {
	page, err := s.deps.Runtime.ListSessionEvents(ctx, request.Params)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return api.ListSessionEvents404JSONResponse{
				ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
					notFoundError(),
				),
			}, nil
		}
		if errors.Is(err, commerr.ErrValidationFailed) {
			return api.ListSessionEvents400JSONResponse{
				ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
					validationError(clientMessage(err)),
				),
			}, nil
		}

		return nil, ctxerrors.Wrap(err, "list session events")
	}

	return api.ListSessionEvents200JSONResponse{
		Body: *page,
		Headers: api.ListSessionEvents200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// ListSessionNotices returns durable notice history without consuming it.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListSessionNotices(
	ctx context.Context,
	request api.ListSessionNoticesRequestObject,
) (api.ListSessionNoticesResponseObject, error) {
	page, err := s.deps.Runtime.ListSessionNotices(ctx, request.Params)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return api.ListSessionNotices404JSONResponse{
				ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
					notFoundError(),
				),
			}, nil
		}
		if errors.Is(err, commerr.ErrValidationFailed) {
			return api.ListSessionNotices400JSONResponse{
				ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
					validationError(clientMessage(err)),
				),
			}, nil
		}

		return nil, ctxerrors.Wrap(err, "list session notices")
	}

	return api.ListSessionNotices200JSONResponse{
		Body: *page,
		Headers: api.ListSessionNotices200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// PublishSessionNotice records an outside report against a session.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) PublishSessionNotice(
	ctx context.Context,
	request api.PublishSessionNoticeRequestObject,
) (api.PublishSessionNoticeResponseObject, error) {
	if request.Body == nil {
		return publishSessionNoticeBadRequest(invalidJSONBodyMessage), nil
	}

	published, err := s.deps.Runtime.PublishSessionNotice(
		ctx,
		request.Params.XSessionID,
		*request.Body,
	)
	if err != nil {
		return s.publishSessionNoticeFailure(err)
	}

	return api.PublishSessionNotice202JSONResponse{
		Body: *published,
		Headers: api.PublishSessionNotice202ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// publishSessionNoticeFailure maps the runtime's typed failures onto the
// documented statuses. A malformed or reserved type is the caller's mistake,
// not a server fault.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) publishSessionNoticeFailure(
	err error,
) (api.PublishSessionNoticeResponseObject, error) {
	switch {
	case errors.Is(err, commerr.ErrNotFound):
		return api.PublishSessionNotice404JSONResponse{
			ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
				notFoundError(),
			),
		}, nil
	case errors.Is(err, events.ErrInvalidType),
		errors.Is(err, events.ErrReservedType),
		errors.Is(err, events.ErrInvalidDelivery),
		errors.Is(err, commerr.ErrValidationFailed):
		return publishSessionNoticeBadRequest(clientMessage(err)), nil
	default:
		return nil, ctxerrors.Wrap(err, "publish session notice")
	}
}

func publishSessionNoticeBadRequest(
	message string,
) api.PublishSessionNotice400JSONResponse {
	return api.PublishSessionNotice400JSONResponse{
		ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
			validationError(message),
		),
	}
}

func listMessagesBadRequest(message string) api.ListMessages400JSONResponse {
	return api.ListMessages400JSONResponse{
		ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
			validationError(message),
		),
	}
}

func listMessagesNotFound() api.ListMessages404JSONResponse {
	return api.ListMessages404JSONResponse{
		ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
			sessionNotFoundError(),
		),
	}
}

func getSessionNotFound() api.GetSession404JSONResponse {
	return api.GetSession404JSONResponse{
		ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
			sessionNotFoundError(),
		),
	}
}

func cancelSessionNotFound() api.CancelSession404JSONResponse {
	return api.CancelSession404JSONResponse{
		ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
			sessionNotFoundError(),
		),
	}
}
