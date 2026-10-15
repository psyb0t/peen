package peen

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// Session addresses one durable conversation owned by a Runtime.
type Session struct {
	runtime *Runtime
	id      uuid.UUID
}

// OpenSessionOptions selects an existing workspace directory.
type OpenSessionOptions struct {
	Workspace string
}

// PageRequest selects a bounded limit/offset page. Zero limit uses the default.
type PageRequest struct {
	Limit  int
	Offset int
}

// SessionPage lists durable session metadata in newest-updated order.
type SessionPage struct {
	Items   []SessionDetails
	Limit   int
	Offset  int
	HasMore bool
}

// OpenSession creates or resumes the session keyed by the canonical workspace.
func (r *Runtime) OpenSession(
	ctx context.Context,
	options OpenSessionOptions,
) (*Session, error) {
	ctx, finish, err := r.internal.BeginOperation(ctx)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open embedded session")
	}
	defer finish()

	openWorkspace := r.store.OpenWorkspace
	if r.options.Filesystem != nil {
		if _, host := r.options.Filesystem.(*HostFS); !host {
			openWorkspace = r.store.OpenWorkspaceIdentity
		}
	}

	opened, err := openWorkspace(ctx, options.Workspace,
		session.OpenSessionOptions{
			RootAgent: r.options.RootAgent,
			ModelID:   r.options.DefaultModel,
		})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open embedded session")
	}

	if _, err := r.workflows.ensure(ctx, opened.Session.ID); err != nil {
		return nil, ctxerrors.Wrap(err, "start opened embedded session")
	}

	return &Session{runtime: r, id: opened.Session.ID}, nil
}

// Session makes a handle without opening storage. Methods validate its ID.
func (r *Runtime) Session(id string) *Session {
	parsed, err := uuid.Parse(id)
	if err != nil {
		parsed = uuid.Nil
	}

	return &Session{runtime: r, id: parsed}
}

// ResumeSession requires an existing durable session.
func (r *Runtime) ResumeSession(
	ctx context.Context,
	id string,
) (*Session, error) {
	result := r.Session(id)
	if _, err := result.Details(ctx); err != nil {
		return nil, ctxerrors.Wrap(err, "resume embedded session")
	}

	return result, nil
}

// ID returns the session's durable identifier.
func (s *Session) ID() string { return s.id.String() }

func (s *Session) begin(ctx context.Context) (
	context.Context, func(), error,
) {
	if s == nil || s.runtime == nil || s.id == uuid.Nil {
		return nil, nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed, "invalid embedded session",
		)
	}

	operation, finish, err := s.runtime.internal.BeginOperation(ctx)
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "begin session operation")
	}

	return operation, finish, nil
}

// Send runs or queues a message. Settings apply only to a new root turn.
func (s *Session) Send(
	ctx context.Context,
	request MessageRequest,
	sink EventSink,
) (MessageResult, error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return MessageResult{}, err
	}
	defer finish()

	input, requestID, err := request.internalRequest(s.id)
	if err != nil {
		return MessageResult{}, err
	}

	var internalSink agent.EventSink
	if sink != nil {
		internalSink = func(event agent.Event) error {
			return sink(Event{
				SessionID: s.ID(), RequestID: event.RequestID.String(),
				Type: event.Type, Payload: event.Payload,
			})
		}
	}

	core, err := s.runtime.workflows.ensure(ctx, s.id)
	if err != nil {
		return MessageResult{}, ctxerrors.Wrap(err, "admit embedded workflow")
	}

	result, err := core.RunMessage(ctx, input,
		requestID, internalSink)
	if err != nil {
		return MessageResult{}, translateTurnError(err)
	}

	return MessageResult{Message: result.Text, Queued: result.Queued}, nil
}

// Details reads session metadata without changing it.
func (s *Session) Details(ctx context.Context) (SessionDetails, error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return SessionDetails{}, err
	}
	defer finish()

	value, err := s.core().Session(ctx, s.id)
	if err != nil {
		return SessionDetails{}, ctxerrors.Wrap(err, "read embedded session")
	}

	return toSessionDetails(*value), nil
}

// Cancel stops the active turn and running children, leaving jobs independent.
func (s *Session) Cancel(ctx context.Context) (CancelResult, error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return CancelResult{}, err
	}
	defer finish()

	value, err := s.core().CancelSession(ctx, s.id)
	if err == nil {
		_, err = s.core().PauseSessionWorkflows(ctx)
	}

	if err != nil {
		return CancelResult{}, ctxerrors.Wrap(err, "cancel embedded session")
	}

	return CancelResult{CancelRequested: value.CancelRequested}, nil
}

// Messages reads a page of the root conversation.
func (s *Session) Messages(
	ctx context.Context, request ListMessagesRequest,
) (ListMessagesResult, error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return ListMessagesResult{}, err
	}
	defer finish()

	params, err := toListMessagesParams(request, s.id)
	if err != nil {
		return ListMessagesResult{}, err
	}

	page, err := s.core().ListMessages(ctx, params)
	if err != nil {
		return ListMessagesResult{}, ctxerrors.Wrap(
			err,
			"read embedded messages",
		)
	}

	return toListMessagesResult(*page)
}

// ListSessions reads every session in this runtime's store.
func (r *Runtime) ListSessions(
	ctx context.Context, request PageRequest,
) (SessionPage, error) {
	ctx, finish, err := r.internal.BeginOperation(ctx)
	if err != nil {
		return SessionPage{}, ctxerrors.Wrap(err, "list runtime sessions")
	}
	defer finish()

	page, err := r.store.ListSessions(ctx, session.ListSessionsOptions{
		Limit: request.Limit, Offset: request.Offset,
	})
	if err != nil {
		return SessionPage{}, ctxerrors.Wrap(err, "list embedded sessions")
	}

	result := SessionPage{
		Limit: page.Limit, Offset: page.Offset,
		HasMore: page.HasMore,
		Items:   make([]SessionDetails, 0, len(page.Items)),
	}
	for _, item := range page.Items {
		value, getErr := r.internal.Session(ctx, item.ID)
		if getErr != nil {
			return SessionPage{}, ctxerrors.Wrap(getErr, "read listed session")
		}

		result.Items = append(result.Items, toSessionDetails(*value))
	}

	return result, nil
}

func (request MessageRequest) internalRequest(
	sessionID uuid.UUID,
) (agent.MessageRequest, uuid.UUID, error) {
	input := agent.MessageRequest{
		SessionID: &sessionID,
		Message:   request.Message,
	}
	if request.Model != "" {
		input.Model = &request.Model
	}

	if request.ReasoningEffort != "" {
		input.ReasoningEffort = &request.ReasoningEffort
	}

	if request.SystemPrompt != nil {
		input.SystemPrompt = &agent.MessageSystemPrompt{
			Mode:    agent.PromptMode(request.SystemPrompt.Mode),
			Content: request.SystemPrompt.Content,
		}
	}

	requestID, err := request.parsedRequestID()
	if err != nil {
		return agent.MessageRequest{}, uuid.Nil, err
	}

	childID, err := request.parsedChildID()
	if err != nil {
		return agent.MessageRequest{}, uuid.Nil, err
	}

	if childID != uuid.Nil {
		input.To = &childID
	}

	return input, requestID, nil
}

func (request MessageRequest) parsedRequestID() (uuid.UUID, error) {
	if request.RequestID == "" {
		return uuid.New(), nil
	}

	return parseRecordID(request.RequestID)
}

func (request MessageRequest) parsedChildID() (uuid.UUID, error) {
	if request.To == "" {
		return uuid.Nil, nil
	}

	id, err := parseRecordID(request.To)
	if err != nil {
		return uuid.Nil, err
	}

	return id, nil
}
