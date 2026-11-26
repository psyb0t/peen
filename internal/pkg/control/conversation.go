package control

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// ConversationRequest selects exactly one existing session or workspace.
// OnEvent observes committed rows for this request only.
type ConversationRequest struct {
	// RequestID correlates controller jobs with their durable turns.
	// Network adapters leave it unset.
	RequestID  uuid.UUID
	SessionID  uuid.UUID
	Workspace  string
	AgentSpace string
	Message    agent.MessageRequest
	OnEvent    func(session.FeedItem) error
}

// ConversationResult carries a terminal worker turn and its persisted output.
type ConversationResult struct {
	SessionID     uuid.UUID
	RequestID     uuid.UUID
	TurnID        uuid.UUID
	State         api.TurnState
	Text          string
	Output        json.RawMessage
	OutputError   string
	Model         string
	LengthLimited bool
	Usage         ConversationUsage
}

// ConversationUsage counts provider tokens across this turn's model calls,
// including billed failed attempts.
type ConversationUsage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// Conversation routes controller callers through the same policy and workers
// as the WebSocket surface. It never runs the controller's model loop.
type Conversation struct {
	registry *Registry
	turns    *TurnRouter
	feed     *FeedPump
}

func NewConversation(
	registry *Registry,
	turns *TurnRouter,
	feed *FeedPump,
) (*Conversation, error) {
	if registry == nil || turns == nil || feed == nil {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"conversation dependencies")
	}

	return &Conversation{registry: registry, turns: turns, feed: feed}, nil
}

// Run waits for this caller's own turn. Cancellation releases the caller
// immediately; scoped worker cleanup follows its admission acknowledgement.
func (c *Conversation) Run(
	ctx context.Context,
	input ConversationRequest,
) (*ConversationResult, error) {
	stored, err := c.resolve(ctx, input)
	if err != nil {
		return nil, err
	}

	requestID := input.RequestID
	if requestID == uuid.Nil {
		requestID = uuid.New()
	}

	stream, stop, err := c.subscribe(ctx, stored.ID, requestID, input.OnEvent)
	if err != nil {
		return nil, err
	}
	defer stop()

	caller, stopCaller := context.WithCancel(ctx)
	defer stopCaller()

	turnContext, stopTurn := c.turnContext(caller)
	admitted, done := make(chan struct{}), make(chan struct{})
	result := make(chan conversationOutcome, 1)

	stopCancellation := c.watchCancellation(caller, stored.ID, requestID,
		admitted, done, stopTurn)
	defer func() {
		if stopCancellation() {
			stopTurn()
		}
	}()

	request := input.Message
	request.RequireNewTurn = true
	request.SessionID = &stored.ID

	go c.route(caller, turnContext, stored.ID, requestID, request,
		admitted, done, result)

	answer, err := c.await(caller, stored.ID, requestID, stream, result)
	if err != nil {
		stopCaller()
	}

	return answer, err
}

func (c *Conversation) turnContext(
	ctx context.Context,
) (context.Context, context.CancelFunc) {
	turn, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stopLifecycle := context.AfterFunc(c.feed.ctx, cancel)

	if c.feed.ctx.Err() != nil {
		cancel()
	}

	return turn, func() {
		stopLifecycle()
		cancel()
	}
}

func (c *Conversation) resolve(
	ctx context.Context,
	input ConversationRequest,
) (*models.Session, error) {
	if err := validateConversationRequest(input); err != nil {
		return nil, err
	}

	if input.SessionID == uuid.Nil {
		opened, err := c.registry.Open(ctx,
			input.Workspace, input.AgentSpace)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "open conversation workspace")
		}

		return c.registry.workspaceSession(ctx, opened.Session.ID)
	}

	return c.registry.workspaceSession(ctx, input.SessionID)
}

func validateConversationRequest(input ConversationRequest) error {
	if (input.SessionID == uuid.Nil) == (input.Workspace == "") ||
		input.Message.System || input.Message.To != nil ||
		input.Message.Origin != nil ||
		(input.SessionID != uuid.Nil &&
			input.AgentSpace != "") {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"conversation requires one regular session or workspace")
	}

	return nil
}

type conversationOutcome struct {
	result *agent.MessageRunResult
	err    error
}

func (c *Conversation) route(
	ctx, turnContext context.Context,
	sessionID, requestID uuid.UUID,
	request agent.MessageRequest,
	admitted, done chan struct{},
	result chan<- conversationOutcome,
) {
	defer close(done)
	defer func() {
		if recovered := recover(); recovered != nil {
			ctxscope.GetLogger(ctx).Error("conversation routing panicked",
				"panic", recovered)

			result <- conversationOutcome{err: ctxerrors.Wrap(
				commerr.ErrExecFailed, "conversation routing panicked")}
		}
	}()

	answer, err := c.turns.runReservedSessionMessage(ctx, turnContext,
		c.turns.ReserveSessionMessage(sessionID), request, requestID,
		sync.OnceFunc(func() { close(admitted) }))
	result <- conversationOutcome{result: answer, err: err}
}

func (c *Conversation) watchCancellation(
	ctx context.Context,
	sessionID, requestID uuid.UUID,
	admitted, done <-chan struct{},
	stopTurn context.CancelFunc,
) func() bool {
	return context.AfterFunc(ctx, func() {
		defer stopTurn()
		defer func() {
			if recovered := recover(); recovered != nil {
				ctxscope.GetLogger(ctx).Error(
					"conversation cancellation panicked",
					"panic", recovered,
				)
			}
		}()

		c.cancelAfterAdmission(ctx, sessionID, requestID, admitted, done)
	})
}

func (c *Conversation) cancelAfterAdmission(
	ctx context.Context,
	sessionID, requestID uuid.UUID,
	admitted, done <-chan struct{},
) {
	select {
	case <-admitted:
	case <-done:
	case <-c.feed.ctx.Done():
		return
	}

	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx),
		conversationCleanupTimeout)
	defer cancel()

	if _, err := c.turns.CancelSessionRequest(cleanup, sessionID,
		requestID); err != nil {
		ctxscope.GetLogger(cleanup).Error("conversation cancellation failed",
			"session_id", sessionID, "request_id", requestID, "err", err)
	}
}

func (c *Conversation) await(
	ctx context.Context,
	sessionID, requestID uuid.UUID,
	stream *conversationFeed,
	result <-chan conversationOutcome,
) (*ConversationResult, error) {
	var pages <-chan []session.FeedItem
	if stream != nil {
		pages = stream.pages
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctxerrors.Wrap(ctx.Err(), "wait for conversation")
		case page, open := <-pages:
			if !open {
				return nil, ctxerrors.Wrap(commerr.ErrInvalidState,
					"conversation feed disconnected")
			}

			if err := stream.observe(page); err != nil {
				return nil, err
			}
		case outcome := <-result:
			return c.complete(ctx, sessionID, requestID, stream, outcome)
		}
	}
}

func (c *Conversation) complete(
	ctx context.Context,
	sessionID, requestID uuid.UUID,
	stream *conversationFeed,
	outcome conversationOutcome,
) (*ConversationResult, error) {
	limited := errors.Is(outcome.err, session.ErrTurnRoundLimit) ||
		errors.Is(outcome.err, session.ErrTurnTokenLimit)
	if outcome.err != nil && !limited {
		return nil, ctxerrors.Wrap(outcome.err, "run conversation turn")
	}

	if stream != nil {
		if err := c.drainFeed(ctx, stream); err != nil {
			return nil, err
		}
	}

	if limited {
		outcome.result = &agent.MessageRunResult{}
	}

	answer, err := c.answer(ctx, sessionID, requestID, outcome.result)
	if err != nil {
		return nil, err
	}

	answer.LengthLimited = limited

	return answer, nil
}

func (c *Conversation) answer(
	ctx context.Context,
	sessionID, requestID uuid.UUID,
	result *agent.MessageRunResult,
) (*ConversationResult, error) {
	turn, err := c.registry.store.GetTurnByRequest(ctx, sessionID, requestID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read conversation outcome")
	}

	if result == nil || result.Queued || turn.State == models.TurnStateRunning {
		return nil, ctxerrors.Wrap(commerr.ErrInvalidState,
			"conversation has no terminal turn")
	}

	answer := &ConversationResult{
		SessionID: sessionID, RequestID: requestID, TurnID: turn.ID,
		State: api.TurnState(turn.State), Text: result.Text,
	}
	if err := c.modelSummary(ctx, answer); err != nil {
		return nil, err
	}

	if turn.Output != nil {
		answer.Output = json.RawMessage(*turn.Output)
	}

	if turn.OutputError != nil {
		answer.OutputError = *turn.OutputError
	}

	return answer, nil
}

func (c *Conversation) modelSummary(
	ctx context.Context,
	answer *ConversationResult,
) error {
	summary, err := c.registry.store.GetTurnModelSummary(ctx,
		answer.SessionID, answer.TurnID)
	if err != nil {
		return ctxerrors.Wrap(err, "read conversation model usage")
	}

	answer.Model = summary.Model

	answer.Usage = ConversationUsage{
		PromptTokens:     summary.PromptTokens,
		CompletionTokens: summary.CompletionTokens,
		TotalTokens:      summary.TotalTokens,
	}
	if answer.Text == "" {
		answer.Text = summary.Text
	}

	return nil
}
