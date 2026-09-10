package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"runtime/debug"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	"github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es/wshub"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/agent"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
)

type webSocketAgentEvent struct {
	SessionID uuid.UUID             `json:"sessionId"`
	RequestID uuid.UUID             `json:"requestId"`
	Event     webSocketAgentPayload `json:"event"`
}

type webSocketAgentPayload struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type webSocketMessageResult struct {
	SessionID uuid.UUID `json:"sessionId"`
	RequestID uuid.UUID `json:"requestId"`
	Queued    bool      `json:"queued"`
}

type webSocketMessageFailure struct {
	SessionID uuid.UUID `json:"sessionId"`
	RequestID uuid.UUID `json:"requestId"`
	Code      string    `json:"code"`
	Message   string    `json:"message"`
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
			logHTTPRejection(
				r.Context(),
				http.StatusNotFound,
				ErrorCodeSessionNotFound,
				err,
			)
			writeAPIError(
				w,
				http.StatusNotFound,
				ErrorCodeSessionNotFound,
				sessionNotFoundError().Message,
			)

			return uuid.Nil, false
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

	request := api.MessageRequest{}
	if err := json.Unmarshal(event.Data, &request); err != nil {
		wrapped := ctxerrors.Wrap(err, "decode websocket message")
		logger.Warn("websocket message rejected", "err", wrapped)
		s.broadcastWebSocketFailure(
			sessionID,
			requestID,
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

	go s.runWebSocketMessage(ctx, request, sessionID, requestID)

	return nil
}

func (s *Server) runWebSocketMessage(
	ctx context.Context,
	request api.MessageRequest,
	sessionID uuid.UUID,
	requestID uuid.UUID,
) {
	logger := ctxscope.GetLogger(ctx)

	defer func() {
		if recovered := recover(); recovered != nil {
			logger.Error(
				"websocket agent message panicked",
				"panic", recovered,
				"stack", string(debug.Stack()),
			)
			s.broadcastWebSocketFailure(
				sessionID,
				requestID,
				aichteeteapee.ErrorCodeInternalServerError,
				webSocketMessageFailedMessage,
			)
		}
	}()

	result, err := s.deps.Runtime.RunMessage(
		ctx,
		request,
		&sessionID,
		requestID,
		func(event agent.Event) error {
			s.broadcastWebSocketAgentEvent(sessionID, requestID, event)

			return nil
		},
	)
	if err != nil {
		wrapped := ctxerrors.Wrap(err, "run websocket agent message")
		logger.Error("websocket agent message failed", "err", wrapped)
		s.broadcastWebSocketFailure(
			sessionID,
			requestID,
			aichteeteapee.ErrorCodeInternalServerError,
			webSocketMessageFailedMessage,
		)

		return
	}

	logger.Debug("websocket agent message completed", "queued", result.Queued)
	s.webSocketHub.BroadcastToClients(
		[]uuid.UUID{sessionID},
		dabluveees.NewEvent(
			webSocketMessageCompletedEventType,
			webSocketMessageResult{
				SessionID: result.SessionID,
				RequestID: requestID,
				Queued:    result.Queued,
			},
		),
	)
}

func (s *Server) broadcastWebSocketAgentEvent(
	sessionID uuid.UUID,
	requestID uuid.UUID,
	event agent.Event,
) {
	s.webSocketHub.BroadcastToClients(
		[]uuid.UUID{sessionID},
		dabluveees.NewEvent(
			webSocketAgentEventType,
			webSocketAgentEvent{
				SessionID: sessionID,
				RequestID: requestID,
				Event: webSocketAgentPayload{
					Type:    event.Type,
					Payload: event.Payload,
				},
			},
		),
	)
}

func (s *Server) broadcastWebSocketFailure(
	sessionID uuid.UUID,
	requestID uuid.UUID,
	code string,
	message string,
) {
	s.webSocketHub.BroadcastToClients(
		[]uuid.UUID{sessionID},
		dabluveees.NewEvent(
			webSocketMessageFailedEventType,
			webSocketMessageFailure{
				SessionID: sessionID,
				RequestID: requestID,
				Code:      code,
				Message:   message,
			},
		),
	)
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
