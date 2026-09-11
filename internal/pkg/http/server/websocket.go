package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"runtime/debug"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	"github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es/wshub"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/agent"
)

type webSocketMessageResult struct {
	Queued bool `json:"queued"`
}

type webSocketMessageFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	sessionID, found := s.requireWebSocketSession(w, r)
	if !found {
		return
	}

	request := r.Clone(r.Context())
	query := request.URL.Query()
	query.Del(webSocketClientIDParameter)
	request.URL.RawQuery = query.Encode()
	request.Header.Set(aichteeteapee.HeaderNameXClientID, sessionID.String())

	ctxscope.GetLogger(r.Context()).Debug(
		"websocket connection accepted",
		"session_id", sessionID.String(),
	)
	s.webSocketUpgradeHandler.ServeHTTP(w, request)
}

func (s *Server) requireWebSocketSession(
	w http.ResponseWriter,
	r *http.Request,
) (uuid.UUID, bool) {
	sessionID, err := webSocketSessionID(r)
	if err != nil {
		logHTTPRejection(
			r.Context(),
			http.StatusBadRequest,
			ErrorCodeInvalidSessionID,
			err,
		)
		writeAPIError(
			w,
			http.StatusBadRequest,
			ErrorCodeInvalidSessionID,
			invalidSessionIDMessage,
		)

		return uuid.Nil, false
	}

	if _, err := s.deps.Runtime.Session(r.Context(), sessionID); err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			ctxscope.GetLogger(r.Context()).Debug(
				"websocket pending session accepted",
				"session_id", sessionID.String(),
			)

			return sessionID, true
		}

		wrapped := ctxerrors.Wrap(err, "read websocket session")
		ctxscope.GetLogger(r.Context()).Error(
			"websocket session lookup failed",
			"err", wrapped,
		)
		writeAPIError(
			w,
			http.StatusInternalServerError,
			aichteeteapee.ErrorCodeInternalServerError,
			internalServerErrorMessage,
		)

		return uuid.Nil, false
	}

	return sessionID, true
}

func webSocketSessionID(r *http.Request) (uuid.UUID, error) {
	sessionID, err := uuid.Parse(r.URL.Query().Get(webSocketSessionIDParameter))
	if err != nil {
		return uuid.Nil, ctxerrors.Wrap(err, "parse websocket session id")
	}

	return sessionID, nil
}

func (s *Server) handleWebSocketMessage(
	_ wshub.Hub,
	client *wshub.Client,
	event *dabluveees.Event,
) error {
	sessionID := client.ID()
	requestID := uuid.New()
	ctx := webSocketContext(s.deps.ServiceContext(), sessionID, requestID)
	logger := ctxscope.GetLogger(ctx)

	request, err := decodeWebSocketMessage(event.Data)
	if err != nil {
		wrapped := ctxerrors.Wrap(err, "decode websocket message")
		logger.Warn("websocket message rejected", "err", wrapped)
		s.broadcastWebSocketFailure(
			sessionID,
			requestID,
			event.ID,
			aichteeteapee.ErrorCodeValidationFailed,
			webSocketMessageRejectedMessage,
		)

		return nil
	}

	logger.Debug(
		"websocket agent message started",
		"event_id", event.ID.String(),
		"event_type", string(event.Type),
	)

	go s.runWebSocketMessage(ctx, request, sessionID, requestID, event.ID)

	return nil
}

func (s *Server) runWebSocketMessage(
	ctx context.Context,
	request agent.MessageRequest,
	sessionID uuid.UUID,
	requestID uuid.UUID,
	triggeringEventID uuid.UUID,
) {
	defer s.recoverWebSocketMessagePanic(
		ctx,
		sessionID,
		requestID,
		triggeringEventID,
	)

	result, err := s.deps.Runtime.RunMessage(
		ctx,
		request,
		&sessionID,
		requestID,
		func(event agent.Event) error {
			s.broadcastWebSocketAgentEvent(
				sessionID,
				requestID,
				triggeringEventID,
				event,
			)

			return nil
		},
	)
	if err != nil {
		s.reportWebSocketMessageFailure(
			ctx,
			sessionID,
			requestID,
			triggeringEventID,
			err,
		)

		return
	}

	ctxscope.GetLogger(ctx).Debug(
		"websocket agent message completed",
		"queued", result.Queued,
	)
	s.broadcastWebSocketMessageCompletion(
		sessionID,
		requestID,
		triggeringEventID,
		result.Queued,
	)
}

func (s *Server) recoverWebSocketMessagePanic(
	ctx context.Context,
	sessionID uuid.UUID,
	requestID uuid.UUID,
	triggeringEventID uuid.UUID,
) {
	if recovered := recover(); recovered != nil {
		ctxscope.GetLogger(ctx).Error(
			"websocket agent message panicked",
			"panic", recovered,
			"stack", string(debug.Stack()),
		)
		s.broadcastWebSocketFailure(
			sessionID,
			requestID,
			triggeringEventID,
			aichteeteapee.ErrorCodeInternalServerError,
			webSocketMessageFailedMessage,
		)
	}
}

func (s *Server) reportWebSocketMessageFailure(
	ctx context.Context,
	sessionID uuid.UUID,
	requestID uuid.UUID,
	triggeringEventID uuid.UUID,
	err error,
) {
	wrapped := ctxerrors.Wrap(err, "run websocket agent message")
	ctxscope.GetLogger(ctx).Error(
		"websocket agent message failed",
		"err", wrapped,
	)

	code, message := webSocketMessageFailureFor(err)
	s.broadcastWebSocketFailure(
		sessionID,
		requestID,
		triggeringEventID,
		code,
		message,
	)
}

func (s *Server) broadcastWebSocketMessageCompletion(
	sessionID uuid.UUID,
	requestID uuid.UUID,
	triggeringEventID uuid.UUID,
	queued bool,
) {
	s.webSocketHub.BroadcastToClients(
		[]uuid.UUID{sessionID},
		newWebSocketEvent(
			webSocketMessageCompletedEventType,
			webSocketMessageResult{Queued: queued},
			sessionID,
			requestID,
			triggeringEventID,
		),
	)
}

func (s *Server) broadcastWebSocketAgentEvent(
	sessionID uuid.UUID,
	requestID uuid.UUID,
	triggeringEventID uuid.UUID,
	event agent.Event,
) {
	s.webSocketHub.BroadcastToClients(
		[]uuid.UUID{sessionID},
		newWebSocketEvent(
			dabluveees.EventType(event.Type),
			event.Payload,
			sessionID,
			requestID,
			triggeringEventID,
		),
	)
}

func (s *Server) broadcastWebSocketFailure(
	sessionID uuid.UUID,
	requestID uuid.UUID,
	triggeringEventID uuid.UUID,
	code aichteeteapee.ErrorCode,
	message string,
) {
	s.webSocketHub.BroadcastToClients(
		[]uuid.UUID{sessionID},
		newWebSocketEvent(
			webSocketMessageFailedEventType,
			webSocketMessageFailure{
				Code:    code,
				Message: message,
			},
			sessionID,
			requestID,
			triggeringEventID,
		),
	)
}

func decodeWebSocketMessage(
	data json.RawMessage,
) (agent.MessageRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	request := agent.MessageRequest{}
	if err := decoder.Decode(&request); err != nil {
		return agent.MessageRequest{}, ctxerrors.Wrap(
			err,
			"decode message payload",
		)
	}

	trailing := struct{}{}
	if err := decoder.Decode(&trailing); err == nil {
		return agent.MessageRequest{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"multiple message payloads",
		)
	} else if !errors.Is(err, io.EOF) {
		return agent.MessageRequest{}, ctxerrors.Wrap(
			err,
			"decode trailing message payload",
		)
	}

	return request, nil
}

func newWebSocketEvent(
	eventType dabluveees.EventType,
	data any,
	sessionID uuid.UUID,
	requestID uuid.UUID,
	triggeringEventID uuid.UUID,
) *dabluveees.Event {
	event := dabluveees.NewEvent(eventType, data).
		SetMetadata(webSocketMetadataSessionID, sessionID.String()).
		SetMetadata(webSocketMetadataRequestID, requestID.String()).
		SetTriggeredBy(triggeringEventID)

	return &event
}

func webSocketMessageFailureFor(
	err error,
) (aichteeteapee.ErrorCode, string) {
	switch {
	case errors.Is(err, commerr.ErrNotFound):
		return ErrorCodeSessionNotFound, sessionNotFoundError().Message
	case errors.Is(err, commerr.ErrValidationFailed),
		errors.Is(err, commerr.ErrRequiredFieldNotSet):
		return aichteeteapee.ErrorCodeValidationFailed,
			webSocketMessageRejectedMessage
	case errors.Is(err, commerr.ErrCancelled):
		return ErrorCodeTurnCancelled, turnCancelledError().Message
	case errors.Is(err, elelem.ErrUserMessageQueueFull):
		return ErrorCodeUserMessageQueueFull,
			userMessageQueueFullError().Message
	case errors.Is(err, commerr.ErrConflict):
		return ErrorCodeSessionBusy, sessionBusyError(
			"session already has an active turn",
		).Message
	default:
		return aichteeteapee.ErrorCodeInternalServerError,
			webSocketMessageFailedMessage
	}
}

func webSocketContext(
	ctx context.Context,
	sessionID uuid.UUID,
	requestID uuid.UUID,
) context.Context {
	return ctxscope.Set(
		ctx,
		ctxscope.Attr("session_id", sessionID.String()),
		ctxscope.Attr("request_id", requestID.String()),
	)
}
