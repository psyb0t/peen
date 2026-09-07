package server

import (
	"context"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/events"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
)

// SendMessage runs one durable agent turn as JSON or an SSE stream.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) SendMessage(
	ctx context.Context,
	request api.SendMessageRequestObject,
) (api.SendMessageResponseObject, error) {
	// A nil Body means oapi-codegen bound no JSON object at all. The
	// whitespace-only message rule is NOT re-checked here: it lives in
	// agent.validateMessageRequest so an embedding Go caller through the
	// future pkg/peen facade gets the same rejection this HTTP edge does.
	if request.Body == nil {
		return sendMessageBadRequest("message is required"), nil
	}

	if wantsStream(ctx) {
		stream, err := s.deps.Runtime.StreamMessage(
			ctx,
			*request.Body,
			request.Params.XSessionID,
			requestID(ctx),
		)
		if err != nil {
			if response, mapped := mapSendMessageError(err); mapped {
				return response, nil
			}

			return nil, ctxerrors.Wrap(err, "stream agent message")
		}

		return api.SendMessage200TexteventStreamResponse{
			Body: stream.Body,
			Headers: api.SendMessage200ResponseHeaders{
				XRequestID: requestID(ctx),
				XSessionID: stream.SessionID,
			},
		}, nil
	}

	result, err := s.deps.Runtime.SendMessage(
		ctx,
		*request.Body,
		request.Params.XSessionID,
		requestID(ctx),
	)
	if err != nil {
		if response, mapped := mapSendMessageError(err); mapped {
			return response, nil
		}

		return nil, ctxerrors.Wrap(err, "send agent message")
	}

	return api.SendMessage200JSONResponse{
		Body: result.Response,
		Headers: api.SendMessage200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: result.SessionID,
		},
	}, nil
}

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

// ListSessionEvents reports what is waiting without consuming it.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListSessionEvents(
	ctx context.Context,
	request api.ListSessionEventsRequestObject,
) (api.ListSessionEventsResponseObject, error) {
	page, err := s.deps.Runtime.ListSessionEvents(
		ctx,
		request.Params.XSessionID,
	)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return api.ListSessionEvents404JSONResponse{
				ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
					notFoundError(),
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

// PublishSessionEvent records an outside report against a session.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) PublishSessionEvent(
	ctx context.Context,
	request api.PublishSessionEventRequestObject,
) (api.PublishSessionEventResponseObject, error) {
	if request.Body == nil {
		return publishSessionEventBadRequest(invalidJSONBodyMessage), nil
	}

	published, err := s.deps.Runtime.PublishSessionEvent(
		ctx,
		request.Params.XSessionID,
		*request.Body,
	)
	if err != nil {
		return s.publishSessionEventFailure(err)
	}

	return api.PublishSessionEvent202JSONResponse{
		Body: *published,
		Headers: api.PublishSessionEvent202ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// publishSessionEventFailure maps the runtime's typed failures onto the
// documented statuses. A malformed or reserved type is the caller's mistake,
// not a server fault.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) publishSessionEventFailure(
	err error,
) (api.PublishSessionEventResponseObject, error) {
	switch {
	case errors.Is(err, commerr.ErrNotFound):
		return api.PublishSessionEvent404JSONResponse{
			ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
				notFoundError(),
			),
		}, nil
	case errors.Is(err, events.ErrInvalidType),
		errors.Is(err, events.ErrReservedType),
		errors.Is(err, events.ErrInvalidDelivery),
		errors.Is(err, commerr.ErrValidationFailed):
		return publishSessionEventBadRequest(clientMessage(err)), nil
	default:
		return nil, ctxerrors.Wrap(err, "publish session event")
	}
}

func publishSessionEventBadRequest(
	message string,
) api.PublishSessionEvent400JSONResponse {
	return api.PublishSessionEvent400JSONResponse{
		ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
			validationError(message),
		),
	}
}

func wantsStream(ctx context.Context) bool {
	stream, _ := ctx.Value(streamContextKey).(bool)

	return stream
}

func sendMessageBadRequest(message string) api.SendMessage400JSONResponse {
	return api.SendMessage400JSONResponse{
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
