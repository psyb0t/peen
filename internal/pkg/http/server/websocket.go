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
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/http/wsapi"
	"github.com/psyb0t/peen/internal/pkg/session"
)

type webSocketMessageResult struct {
	Queued bool `json:"queued"`
}

type webSocketMessageFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Reason  string `json:"reason,omitempty"`
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
		s.rejectWebSocketSessionRouting(client.ID(), event.ID, requestID, err)

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
			ctx,
			client.ID(),
			&failedSessionID,
			requestID,
			event.ID,
			newWebSocketMessageFailure(
				aichteeteapee.ErrorCodeValidationFailed,
				webSocketMessageRejectedMessage,
				"",
			),
		)

		return nil
	}

	request.SourceEventID = event.ID

	logger.Debug(
		"websocket agent message started",
		"event_id", event.ID.String(),
		"event_type", string(event.Type),
	)

	reservation := s.deps.Turns.ReserveSessionMessage(sessionID)

	go s.runWebSocketMessage(ctx, reservation, request, requestID, event.ID)

	return nil
}

func (s *Server) rejectWebSocketSessionRouting(
	clientID uuid.UUID,
	triggeringEventID uuid.UUID,
	requestID uuid.UUID,
	err error,
) {
	ctx := s.deps.ServiceContext()

	ctxscope.GetLogger(ctx).Warn(
		"websocket message rejected",
		"reason", "session_routing",
		"err", err,
	)
	s.broadcastWebSocketFailureToClient(
		ctx,
		clientID,
		nil,
		requestID,
		triggeringEventID,
		newWebSocketMessageFailure(
			aichteeteapee.ErrorCodeValidationFailed,
			webSocketMessageRejectedMessage,
			"",
		),
	)
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
	reservation *session.AdmissionTicket,
	request agent.MessageRequest,
	requestID uuid.UUID,
	triggeringEventID uuid.UUID,
) {
	sessionID := reservation.SessionID()

	// A panic before the router takes the reservation must still free the
	// session for the messages behind this one. Releasing twice is harmless.
	defer reservation.Release()
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
	result, err := s.deps.Turns.RunReservedSessionMessage(
		ctx,
		reservation,
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
		ctx,
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
			ctx,
			sessionID,
			requestID,
			triggeringEventID,
			newWebSocketMessageFailure(
				aichteeteapee.ErrorCodeInternalServerError,
				webSocketMessageFailedMessage,
				"",
			),
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

	failure := webSocketMessageFailureFor(err)
	s.broadcastWebSocketFailure(
		ctx,
		sessionID,
		requestID,
		triggeringEventID,
		failure,
	)
}

func (s *Server) broadcastWebSocketMessageCompletion(
	ctx context.Context,
	sessionID uuid.UUID,
	requestID uuid.UUID,
	triggeringEventID uuid.UUID,
	queued bool,
) {
	s.broadcastWebSocketEvent(
		ctx,
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
	ctx context.Context,
	sessionID uuid.UUID,
	requestID uuid.UUID,
	triggeringEventID uuid.UUID,
	failure webSocketMessageFailure,
) {
	s.broadcastWebSocketEvent(
		ctx,
		sessionID,
		newWebSocketEvent(
			webSocketMessageFailedEventType,
			failure,
			sessionID,
			requestID,
			triggeringEventID,
		),
	)
}

func (s *Server) broadcastWebSocketFailureToClient(
	ctx context.Context,
	clientID uuid.UUID,
	sessionID *uuid.UUID,
	requestID uuid.UUID,
	triggeringEventID uuid.UUID,
	failure webSocketMessageFailure,
) {
	event := dabluveees.NewEvent(
		webSocketMessageFailedEventType,
		failure,
	).SetMetadata(webSocketMetadataRequestID, requestID.String()).
		SetTriggeredBy(triggeringEventID)
	if sessionID != nil {
		event = event.SetMetadata(
			webSocketMetadataSessionID,
			sessionID.String(),
		)
	}

	validateServerFrame(ctx, &event)

	s.webSocketHub.BroadcastToClients([]uuid.UUID{clientID}, &event)
}

func (s *Server) broadcastWebSocketEvent(
	ctx context.Context,
	sessionID uuid.UUID,
	event *dabluveees.Event,
) {
	validateServerFrame(ctx, event)

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

// decodeWebSocketMessage decodes a message.send event's data through the
// generated wsapi strict codec, which rejects an unknown field, a missing
// message, an out-of-range length, and an unknown reasoningEffort the same
// way the hand-written decoder used to. The result is mapped onto
// agent.MessageRequest so the rest of the turn path stays transport-neutral.
func decodeWebSocketMessage(
	data json.RawMessage,
) (agent.MessageRequest, error) {
	payload := wsapi.MessageSendData{}
	if err := payload.UnmarshalJSON(data); err != nil {
		return agent.MessageRequest{}, ctxerrors.Wrap(
			err,
			"decode message payload",
		)
	}

	request, err := messageSendDataToRequest(payload)
	if err != nil {
		return agent.MessageRequest{}, ctxerrors.Wrap(
			err,
			"convert message payload",
		)
	}

	return request, nil
}

// messageSendDataToRequest maps a decoded MessageSendData onto the
// transport-neutral agent.MessageRequest. Field-level shape and length are
// already proven by UnmarshalJSON; only "to", which wsapi carries as an
// unenforced string, needs its own uuid parse here.
func messageSendDataToRequest(
	data wsapi.MessageSendData,
) (agent.MessageRequest, error) {
	request := agent.MessageRequest{Message: data.Message}

	if data.Model != nil {
		request.Model = data.Model
	}

	if data.ReasoningEffort != nil {
		effort := string(*data.ReasoningEffort)
		request.ReasoningEffort = &effort
	}

	if data.SystemPrompt != nil {
		request.SystemPrompt = &agent.MessageSystemPrompt{
			Content: data.SystemPrompt.Content,
			Mode:    agent.PromptMode(data.SystemPrompt.Mode),
		}
	}

	if data.To != nil {
		to, err := uuid.Parse(*data.To)
		if err != nil {
			return agent.MessageRequest{}, ctxerrors.Wrap(err, "parse to")
		}

		request.To = &to
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

func webSocketMessageFailureFor(err error) webSocketMessageFailure {
	// A specific failure is checked before the class it also belongs to.
	specificFailures := []func(error) (webSocketMessageFailure, bool){
		harnessConfigurationFailureFor,
		runningTurnFailureFor,
		unknownSkillFailureFor,
		agentRunFailureFor,
	}

	for _, specificFailureFor := range specificFailures {
		if failure, found := specificFailureFor(err); found {
			return failure
		}
	}

	switch {
	case errors.Is(err, commerr.ErrNotFound):
		return newWebSocketMessageFailure(
			ErrorCodeSessionNotFound,
			sessionNotFoundError().Message,
			"",
		)
	case errors.Is(err, commerr.ErrValidationFailed),
		errors.Is(err, commerr.ErrRequiredFieldNotSet):
		return newWebSocketMessageFailure(
			aichteeteapee.ErrorCodeValidationFailed,
			webSocketMessageRejectedMessage,
			"",
		)
	case errors.Is(err, commerr.ErrCancelled):
		return newWebSocketMessageFailure(
			ErrorCodeTurnCancelled,
			turnCancelledError().Message,
			"",
		)
	case errors.Is(err, elelem.ErrUserMessageQueueFull):
		return newWebSocketMessageFailure(
			ErrorCodeUserMessageQueueFull,
			userMessageQueueFullError().Message,
			"",
		)
	case errors.Is(err, commerr.ErrConflict):
		return newWebSocketMessageFailure(
			ErrorCodeSessionBusy,
			sessionBusyError("session already has an active turn").Message,
			"",
		)
	default:
		return newWebSocketMessageFailure(
			aichteeteapee.ErrorCodeInternalServerError,
			webSocketMessageFailedMessage,
			"",
		)
	}
}

// agentRunFailureFor reports a message for a child agent that ended or never
// existed. Both are checked before their classes, which would otherwise read
// as a missing session or a busy one.
func agentRunFailureFor(err error) (webSocketMessageFailure, bool) {
	switch {
	case errors.Is(err, session.ErrAgentRunFinished):
		return newWebSocketMessageFailure(
			ErrorCodeAgentFinished,
			webSocketAgentFinishedMessage,
			"",
		), true
	case errors.Is(err, session.ErrAgentRunNotFound):
		return newWebSocketMessageFailure(
			ErrorCodeAgentNotFound,
			webSocketAgentNotFoundMessage,
			"",
		), true
	default:
		return webSocketMessageFailure{}, false
	}
}

// runningTurnFailureFor reports a message refused because it tried to change
// the turn it would have joined. It is invalid input, not a busy session, and
// its sentinel text is written for the sender, so the client shows it as is.
func runningTurnFailureFor(err error) (webSocketMessageFailure, bool) {
	if !errors.Is(err, session.ErrRunningTurnSettingsChange) {
		return webSocketMessageFailure{}, false
	}

	return newWebSocketMessageFailure(
		aichteeteapee.ErrorCodeValidationFailed,
		session.ErrRunningTurnSettingsChange.Error(),
		"",
	), true
}

// unknownSkillFailureFor reports a message that named a skill that does not
// exist. The error's message is written for the sender and holds no source
// location, so the client shows it as is.
func unknownSkillFailureFor(err error) (webSocketMessageFailure, bool) {
	var unknown *harness.UnknownSkillError
	if !errors.As(err, &unknown) {
		return webSocketMessageFailure{}, false
	}

	return newWebSocketMessageFailure(
		aichteeteapee.ErrorCodeValidationFailed,
		unknown.Message,
		"",
	), true
}

func harnessConfigurationFailureFor(
	err error,
) (webSocketMessageFailure, bool) {
	switch {
	case errors.Is(err, harness.ErrInvalidSkill):
		return harnessConfigurationFailure(webSocketInvalidSkillReason), true
	case errors.Is(err, harness.ErrInvalidInstruction):
		return harnessConfigurationFailure(
			webSocketInvalidInstructionReason,
		), true
	case errors.Is(err, harness.ErrInvalidAgent):
		return harnessConfigurationFailure(webSocketInvalidAgentReason), true
	case errors.Is(err, harness.ErrInvalidEventHandler),
		errors.Is(err, harness.ErrInvalidHook):
		return harnessConfigurationFailure(webSocketInvalidHookReason), true
	case errors.Is(err, harness.ErrResourceLimit):
		return harnessConfigurationFailure(webSocketHarnessLimitReason), true
	case errors.Is(err, harness.ErrUnreadableLayer):
		return harnessConfigurationFailure(
			webSocketUnreadableHarnessReason,
		), true
	default:
		return webSocketMessageFailure{}, false
	}
}

func harnessConfigurationFailure(reason string) webSocketMessageFailure {
	return newWebSocketMessageFailure(
		ErrorCodeHarnessConfigurationInvalid,
		webSocketHarnessConfigurationMessage,
		reason,
	)
}

func newWebSocketMessageFailure(
	code aichteeteapee.ErrorCode,
	message string,
	reason string,
) webSocketMessageFailure {
	return webSocketMessageFailure{
		Code:    code,
		Message: message,
		Reason:  reason,
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
