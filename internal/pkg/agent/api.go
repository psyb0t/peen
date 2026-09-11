package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// API is Peen's transport-neutral core contract. HTTP and embedding adapters
// depend on this surface rather than runtime storage internals.
//
// It is composed from one interface per concern so a consumer that only needs
// part of it, such as a future public facade, can depend on the narrow piece
// instead of the whole runtime.
type API interface {
	MessageAPI
	SessionAPI
	SessionEventAPI
	ProcessJobAPI
	RunAPI
}

// MessageAPI runs turns and reads the durable transcript.
type MessageAPI interface {
	RunMessage(
		ctx context.Context,
		request MessageRequest,
		sessionID *uuid.UUID,
		requestID uuid.UUID,
		sink EventSink,
	) (*MessageRunResult, error)
	ListMessages(
		ctx context.Context,
		params api.ListMessagesParams,
	) (*api.MessagePage, error)
}

// SessionAPI reads session metadata and stops an active turn.
type SessionAPI interface {
	Session(ctx context.Context, sessionID uuid.UUID) (*api.Session, error)
	CancelSession(
		ctx context.Context,
		sessionID uuid.UUID,
	) (*api.CancelResponse, error)
}

// SessionEventAPI reads and publishes the notices a session accumulates.
type SessionEventAPI interface {
	ListSessionEvents(
		ctx context.Context,
		sessionID uuid.UUID,
	) (*api.SessionEventPage, error)
	PublishSessionEvent(
		ctx context.Context,
		sessionID uuid.UUID,
		request api.SessionEventRequest,
	) (*api.SessionEvent, error)
}

// ProcessJobAPI observes and stops the commands a session started.
type ProcessJobAPI interface {
	ListSessionJobs(
		ctx context.Context,
		sessionID uuid.UUID,
		params api.ListSessionJobsParams,
	) (*api.JobPage, error)
	ReadSessionJobOutput(
		ctx context.Context,
		sessionID uuid.UUID,
		jobID uuid.UUID,
		params api.ReadSessionJobOutputParams,
	) (*api.JobOutput, error)
	SignalSessionJob(
		ctx context.Context,
		sessionID uuid.UUID,
		jobID uuid.UUID,
		request api.JobSignalRequest,
	) (*api.JobSignalResponse, error)
}

// RunAPI observes and cancels the child agent runs a session launched.
type RunAPI interface {
	ListSessionAgentRuns(
		ctx context.Context,
		sessionID uuid.UUID,
		params api.ListSessionAgentRunsParams,
	) (*api.AgentRunPage, error)
	ListSessionAgentRunEvents(
		ctx context.Context,
		sessionID uuid.UUID,
		agentRunID uuid.UUID,
		params api.ListSessionAgentRunEventsParams,
	) (*api.AgentRunEventPage, error)
	CancelSessionAgentRun(
		ctx context.Context,
		sessionID uuid.UUID,
		agentRunID uuid.UUID,
	) (*api.AgentRunCancelResponse, error)
}

// RunMessage runs one completed agent turn while forwarding each visible event
// to sink. A nil sink is useful to an embedding that needs durable results but
// no live event transport.
func (r *Runtime) RunMessage(
	ctx context.Context,
	request MessageRequest,
	sessionID *uuid.UUID,
	requestID uuid.UUID,
	sink EventSink,
) (*MessageRunResult, error) {
	input, err := messageRequestToTurnRequest(request, sessionID, requestID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "convert message request")
	}

	input.OnEvent = sink

	result, err := r.Run(ctx, input)
	if err != nil {
		return nil, translateOperationError(err)
	}

	return &MessageRunResult{
		SessionID: result.SessionID,
		Queued:    result.Queued,
		Text:      result.Text,
	}, nil
}

// Session reads one durable session and its current in-process turn state.
func (r *Runtime) Session(
	ctx context.Context,
	sessionID uuid.UUID,
) (*api.Session, error) {
	stored, err := r.store.Get(ctx, sessionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get session")
	}

	result := sessionToAPI(stored, r.store.IsActive(sessionID))

	return &result, nil
}

// ListMessages reads one durable transcript page as the public API model.
func (r *Runtime) ListMessages(
	ctx context.Context,
	params api.ListMessagesParams,
) (*api.MessagePage, error) {
	options, err := listMessagesOptionsFromAPI(params)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "convert list messages parameters")
	}

	page, err := r.store.ListMessages(ctx, params.XSessionID, options)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list session messages")
	}

	converted, err := messagePageToAPI(page)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "convert message page")
	}

	return &converted, nil
}

// CancelSession requests cancellation for the session's active turn.
func (r *Runtime) CancelSession(
	ctx context.Context,
	sessionID uuid.UUID,
) (*api.CancelResponse, error) {
	cancelRequested, err := r.store.Cancel(ctx, sessionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "cancel session")
	}

	return &api.CancelResponse{CancelRequested: cancelRequested}, nil
}

func messageRequestToTurnRequest(
	request MessageRequest,
	sessionID *uuid.UUID,
	requestID uuid.UUID,
) (TurnRequest, error) {
	if err := validateMessageRequest(request); err != nil {
		return TurnRequest{}, err
	}

	input := TurnRequest{
		Message:   request.Message,
		Model:     optionalString(request.Model),
		RequestID: requestID,
		SessionID: sessionID,
		Workspace: optionalString(request.Workspace),
	}
	if request.SystemPrompt != nil {
		mode, err := promptModeFromMessageRequest(request.SystemPrompt)
		if err != nil {
			return TurnRequest{}, err
		}

		input.SystemPrompt = request.SystemPrompt.Content
		input.SystemPromptMode = mode
	}

	return input, nil
}

// validateMessageRequest rejects a whitespace-only message. The OpenAPI
// minLength constraint on message cannot express this: minLength counts
// characters, not meaningful content, so a string of spaces still satisfies
// it. This check lives here rather than in the HTTP handler because the
// runtime is also reachable directly by an embedding Go caller through the
// future pkg/peen facade, and that caller must see the same rejection the
// HTTP edge does.
func validateMessageRequest(request MessageRequest) error {
	if strings.TrimSpace(request.Message) == "" {
		return ctxerrors.Wrap(commerr.ErrValidationFailed, "message")
	}

	if err := validateOptionalRequestValue(
		request.Workspace,
		"workspace",
	); err != nil {
		return err
	}

	if err := validateOptionalRequestValue(request.Model, "model"); err != nil {
		return err
	}

	return nil
}

func validateOptionalRequestValue(value *string, field string) error {
	if value == nil || strings.TrimSpace(*value) != "" {
		return nil
	}

	return ctxerrors.Wrap(commerr.ErrValidationFailed, field)
}

func promptModeFromMessageRequest(
	prompt *MessageSystemPrompt,
) (PromptMode, error) {
	if strings.TrimSpace(prompt.Content) == "" {
		return "", ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"system prompt content",
		)
	}

	switch prompt.Mode {
	case PromptModeAppend:
		return PromptModeAppend, nil
	case PromptModeReplace:
		return PromptModeReplace, nil
	default:
		return "", ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"system prompt mode",
		)
	}
}

func listMessagesOptionsFromAPI(
	params api.ListMessagesParams,
) (session.ListMessagesOptions, error) {
	options := session.ListMessagesOptions{
		Limit:  session.DefaultPageLimit,
		Offset: 0,
		Order:  session.PageOrderAscending,
	}
	if params.Limit != nil {
		options.Limit = int(*params.Limit)
	}

	if params.Offset != nil {
		options.Offset = int(*params.Offset)
	}

	if params.Order != nil {
		switch *params.Order {
		case api.ListMessagesParamsOrderAsc:
			options.Order = session.PageOrderAscending
		case api.ListMessagesParamsOrderDesc:
			options.Order = session.PageOrderDescending
		default:
			return session.ListMessagesOptions{}, ctxerrors.Wrap(
				commerr.ErrValidationFailed,
				"message order",
			)
		}
	}

	if options.Limit < 1 || options.Limit > session.MaximumPageLimit ||
		options.Offset < 0 {
		return session.ListMessagesOptions{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"message page",
		)
	}

	return options, nil
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}

	return *value
}

func translateOperationError(err error) error {
	if errors.Is(err, session.ErrSessionBusy) {
		return ctxerrors.Wrap(
			commerr.ErrConflict,
			"session already has an active turn",
		)
	}

	// finalizeFailedTurn already classified this failure and made the
	// classification durable before returning it here. Re-running
	// failedTurnState against the same error reads off that one decision
	// instead of a second, HTTP-edge-local context.Canceled check drifting
	// out of sync with it.
	_, _, eventType := failedTurnState(err)
	if eventType == EventTypeTurnCancelled {
		return ctxerrors.Wrap(commerr.ErrCancelled, "turn was cancelled")
	}

	return err
}

var _ API = (*Runtime)(nil)
