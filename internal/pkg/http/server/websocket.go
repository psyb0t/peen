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
	"github.com/psyb0t/peen/internal/pkg/session"
)

type webSocketMessageResult struct {
	Queued bool `json:"queued"`
}

type webSocketMessageFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

const webSocketSessionIDVersion uuid.Version = 4

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	filterSessionID, hasSessionFilter, err := webSocketSessionFilter(r)
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

		return
	}

	request := r.Clone(r.Context())
	query := request.URL.Query()
	query.Del(webSocketClientIDParameter)
	query.Del(webSocketSessionIDParameter)
	request.URL.RawQuery = query.Encode()
	request.Header.Del(aichteeteapee.HeaderNameXClientID)

	filteredClientID := uuid.Nil
	if hasSessionFilter {
		filteredClientID = uuid.New()
		request.Header.Set(
			aichteeteapee.HeaderNameXClientID,
			filteredClientID.String(),
		)
		s.setWebSocketFilter(filteredClientID, filterSessionID)
	}

	logger := ctxscope.GetLogger(r.Context())
	if !hasSessionFilter {
		logger.Debug("websocket global connection accepted")
	} else {
		logger.Debug(
			"websocket filtered connection accepted",
			"session_id", filterSessionID.String(),
		)
	}

	s.webSocketUpgradeHandler.ServeHTTP(w, request)

	if hasSessionFilter &&
		s.webSocketHub.GetClient(filteredClientID) == nil {
		s.clearWebSocketFilter(filteredClientID)
	}
}

func webSocketSessionFilter(r *http.Request) (uuid.UUID, bool, error) {
	values, found := r.URL.Query()[webSocketSessionIDParameter]
	if !found {
		return uuid.Nil, false, nil
	}

	if len(values) != 1 {
		return uuid.Nil, false, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"websocket session filter",
		)
	}

	sessionID, err := parseWebSocketSessionID(values[0])
	if err != nil {
		return uuid.Nil, false, ctxerrors.Wrap(
			err,
			"parse websocket session filter",
		)
	}

	return sessionID, true, nil
}

func (s *Server) handleWebSocketMessage(
	_ wshub.Hub,
	client *wshub.Client,
	event *dabluveees.Event,
) error {
	requestID := uuid.New()

	sessionID, err := s.resolveWebSocketSession(event)
	if err != nil {
		ctxscope.GetLogger(s.deps.ServiceContext()).Warn(
			"websocket message rejected",
			"reason", "session_routing",
			"err", err,
		)
		s.broadcastWebSocketFailureToClient(
			client.ID(),
			nil,
			requestID,
			event.ID,
			aichteeteapee.ErrorCodeValidationFailed,
			webSocketMessageRejectedMessage,
		)

		return nil
	}

	ctx := webSocketContext(s.deps.ServiceContext(), sessionID, requestID)
	logger := ctxscope.GetLogger(ctx)

	request, err := decodeWebSocketMessage(event.Data)
	if err != nil {
		wrapped := ctxerrors.Wrap(err, "decode websocket message")
		failedSessionID := sessionID

		logger.Warn("websocket message rejected", "err", wrapped)
		s.broadcastWebSocketFailureToClient(
			client.ID(),
			&failedSessionID,
			requestID,
			event.ID,
			aichteeteapee.ErrorCodeValidationFailed,
			webSocketMessageRejectedMessage,
		)

		return nil
	}

	request.SourceEventID = event.ID

	logger.Debug(
		"websocket agent message started",
		"event_id", event.ID.String(),
		"event_type", string(event.Type),
	)

	go s.runWebSocketMessage(ctx, request, sessionID, requestID, event.ID)

	return nil
}

// resolveWebSocketSession decides which durable session a message.send runs
// against.
//
// A control surface serves many workspaces, so the client names the session in
// the event's sessionId metadata. Naming it is routing, not an override: the
// runtime still loads the session before starting a turn, and an ID the caller
// is not entitled to fails there rather than here.
//
// An event that names no session falls back to the runtime's own startup
// session, which is what an embedded one-workspace runtime has. A control
// surface has none, so a message that names no session is refused instead of
// running against an arbitrary workspace.
func (s *Server) resolveWebSocketSession(
	event *dabluveees.Event,
) (uuid.UUID, error) {
	selected, found, err := webSocketEventSession(event)
	if err != nil {
		return uuid.Nil, err
	}

	if found {
		return selected, nil
	}

	fallback := s.deps.Runtime.SessionID()
	if fallback == uuid.Nil {
		return uuid.Nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"message.send requires a sessionId",
		)
	}

	return fallback, nil
}

func webSocketEventSession(
	event *dabluveees.Event,
) (uuid.UUID, bool, error) {
	if event == nil || event.Metadata == nil {
		return uuid.Nil, false, nil
	}

	raw, found := event.Metadata.Get(webSocketMetadataSessionID)
	if !found {
		return uuid.Nil, false, nil
	}

	text, isText := raw.(string)
	if !isText {
		return uuid.Nil, false, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"sessionId metadata must be a string",
		)
	}

	sessionID, err := parseWebSocketSessionID(text)
	if err != nil {
		return uuid.Nil, false, ctxerrors.Wrap(err, "parse sessionId metadata")
	}

	return sessionID, true, nil
}

func parseWebSocketSessionID(value string) (uuid.UUID, error) {
	sessionID, err := uuid.Parse(value)
	if err != nil || sessionID == uuid.Nil ||
		sessionID.Variant() != uuid.RFC4122 ||
		sessionID.Version() != webSocketSessionIDVersion ||
		sessionID.String() != value {
		return uuid.Nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"websocket session id",
		)
	}

	return sessionID, nil
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

	// The routed session comes from the transport, never from the decoded
	// message body, so a message cannot redirect itself to another session.
	request.SessionID = &sessionID

	// The turn runs in the session's worker. Its events reach this feed after
	// the controller has written them, through the event relay, rather than
	// through a second delivery path from here.
	result, err := s.deps.Turns.RunSessionMessage(
		ctx,
		sessionID,
		request,
		requestID,
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
	s.broadcastWebSocketEvent(
		sessionID,
		newWebSocketEvent(
			webSocketMessageCompletedEventType,
			webSocketMessageResult{Queued: queued},
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
	s.broadcastWebSocketEvent(
		sessionID,
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

func (s *Server) broadcastWebSocketFailureToClient(
	clientID uuid.UUID,
	sessionID *uuid.UUID,
	requestID uuid.UUID,
	triggeringEventID uuid.UUID,
	code aichteeteapee.ErrorCode,
	message string,
) {
	event := dabluveees.NewEvent(
		webSocketMessageFailedEventType,
		webSocketMessageFailure{Code: code, Message: message},
	).SetMetadata(webSocketMetadataRequestID, requestID.String()).
		SetTriggeredBy(triggeringEventID)
	if sessionID != nil {
		event = event.SetMetadata(
			webSocketMetadataSessionID,
			sessionID.String(),
		)
	}

	s.webSocketHub.BroadcastToClients([]uuid.UUID{clientID}, &event)
}

// BroadcastDurableSessionEvents fans out events the controller has already
// written.
//
// It is the live half of the worker event path: a worker's durable write lands
// in SQLite first, and only then does the controller hand the same records here
// for delivery. A client therefore never sees an event the database does not
// already hold.
func (s *Server) BroadcastDurableSessionEvents(
	sessionID uuid.UUID,
	events []session.EventInput,
) {
	for _, event := range events {
		s.broadcastWebSocketEvent(
			sessionID,
			newWebSocketEvent(
				dabluveees.EventType(event.EventType),
				json.RawMessage(event.PayloadJSON),
				sessionID,
				event.RequestID,
				uuid.Nil,
			),
		)
	}
}

func (s *Server) broadcastWebSocketEvent(
	sessionID uuid.UUID,
	event *dabluveees.Event,
) {
	clientIDs := s.webSocketClientsFor(sessionID)
	if len(clientIDs) == 0 {
		return
	}

	s.webSocketHub.BroadcastToClients(clientIDs, event)
}

// webSocketSessionFilterEntry is one client's server-side session filter.
//
// registered records that the hub has actually held this client, which is what
// makes it safe to drop the filter once the hub no longer has it. The filter is
// recorded before the upgrade runs, so an entry that has never been registered
// is a connection still being established, not a stale one.
type webSocketSessionFilterEntry struct {
	sessionID  uuid.UUID
	registered bool
}

func (s *Server) setWebSocketFilter(
	clientID uuid.UUID,
	sessionID uuid.UUID,
) {
	s.webSocketFilterMutex.Lock()
	defer s.webSocketFilterMutex.Unlock()

	s.webSocketFilters[clientID] = webSocketSessionFilterEntry{
		sessionID: sessionID,
	}
}

func (s *Server) clearWebSocketFilter(clientID uuid.UUID) {
	s.webSocketFilterMutex.Lock()
	defer s.webSocketFilterMutex.Unlock()

	delete(s.webSocketFilters, clientID)
}

func (s *Server) webSocketClientsFor(sessionID uuid.UUID) []uuid.UUID {
	clients := s.webSocketHub.GetAllClients()
	clientIDs := make([]uuid.UUID, 0, len(clients))

	s.webSocketFilterMutex.Lock()
	defer s.webSocketFilterMutex.Unlock()

	// Dropping a filter whose client the hub never held would turn a client
	// that asked for one session into a global subscriber the moment its
	// upgrade finished, which is the opposite of what it requested.
	for clientID, entry := range s.webSocketFilters {
		_, active := clients[clientID]

		switch {
		case active && !entry.registered:
			entry.registered = true
			s.webSocketFilters[clientID] = entry
		case !active && entry.registered:
			delete(s.webSocketFilters, clientID)
		}
	}

	for clientID := range clients {
		entry, filtered := s.webSocketFilters[clientID]
		if filtered && entry.sessionID != sessionID {
			continue
		}

		clientIDs = append(clientIDs, clientID)
	}

	return clientIDs
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
