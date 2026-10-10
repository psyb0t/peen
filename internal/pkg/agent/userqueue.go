package agent

import (
	"context"
	"errors"
	"sync"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// activeUserMessageQueue joins Elelem's in-memory delivery queue to the
// active turn's durable transcript. Its mutex serializes acceptance against
// turn completion and preserves user-message order among concurrent callers.
type activeUserMessageQueue struct {
	mutex     sync.Mutex
	queue     *elelem.UserMessageQueue
	turn      *runtimeTurn
	workspace string
	capacity  int
	closed    bool
	pending   []queuedUserMessage

	// modelReference, model, and reasoningEffort are the running turn's
	// effective settings. A queued message may repeat them but not change
	// them. They are fixed when the queue is registered, so reading them
	// needs no lock.
	modelReference  string
	model           ModelClient
	reasoningEffort elelem.ReasoningEffort
}

type queuedUserMessage struct {
	message       string
	requestID     uuid.UUID
	sourceEventID uuid.UUID
}

type userMessagePayload struct {
	Message       string `json:"message"`
	SourceEventID string `json:"sourceEventId,omitempty"`
}

func newActiveUserMessageQueue(
	prepared *preparedTurn,
	capacity int,
) (*activeUserMessageQueue, error) {
	queue, err := elelem.NewUserMessageQueue(capacity)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create active user message queue")
	}

	return &activeUserMessageQueue{
		queue:          queue,
		turn:           prepared.turn,
		workspace:      prepared.workspace,
		capacity:       capacity,
		modelReference: prepared.modelReference,
		model:          prepared.model,
		reasoningEffort: effectiveReasoningEffort(
			prepared.reasoningEffort,
			prepared.model,
		),
	}, nil
}

// changesRunningTurn reports whether a queued message asks the running turn to
// run differently. An empty model or reasoning level asks for nothing. A value
// that resolves to the running turn's own, such as the default model named in
// full or a level the model fits to the same one, is no change either.
func (q *activeUserMessageQueue) changesRunningTurn(input TurnRequest) bool {
	if input.SystemPrompt != "" || input.SystemPromptMode != "" {
		return true
	}

	if input.Model != "" && input.Model != q.modelReference {
		return true
	}

	if input.ReasoningEffort == elelem.ReasoningEffortUnset {
		return false
	}

	return effectiveReasoningEffort(input.ReasoningEffort, q.model) !=
		q.reasoningEffort
}

// effectiveReasoningEffort is the level a model call on model actually
// carries when requested is asked for.
func effectiveReasoningEffort(
	requested elelem.ReasoningEffort,
	model ModelClient,
) elelem.ReasoningEffort {
	// The reason only explains a difference for a log line, which the turn
	// itself writes when it fits the level for its model call.
	effort, _ := fitReasoningEffort(
		requested,
		model.Model,
		model.Client.Capabilities(model.Model),
	)

	return effort
}

func (q *activeUserMessageQueue) enqueue(
	ctx context.Context,
	input TurnRequest,
) error {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	if q.closed {
		return ctxerrors.Wrap(commerr.ErrConflict, "active turn has completed")
	}

	if q.queue.Len() >= q.capacity {
		return ctxerrors.Wrap(
			errors.Join(commerr.ErrConflict, elelem.ErrUserMessageQueueFull),
			"queue active user message",
		)
	}

	payload := userMessagePayload{Message: input.Message}
	if input.SourceEventID != uuid.Nil {
		payload.SourceEventID = input.SourceEventID.String()
	}

	if err := q.emitAccepted(ctx, input, payload); err != nil {
		return err
	}

	if err := q.queue.EnqueueText(input.Message); err != nil {
		return ctxerrors.Wrap(err, "enqueue active user message")
	}

	q.pending = append(q.pending, queuedUserMessage{
		message:       input.Message,
		requestID:     input.RequestID,
		sourceEventID: input.SourceEventID,
	})

	loggerContext := ctxscope.Set(
		ctx,
		ctxscope.Attr("session_id", q.turn.lease.SessionID.String()),
	)
	ctxscope.GetLogger(loggerContext).Info(
		"active turn user message queued",
		"message_bytes", len(input.Message),
		"queued_depth", q.queue.Len(),
	)

	return nil
}

func (q *activeUserMessageQueue) emitAccepted(
	ctx context.Context,
	input TurnRequest,
	payload userMessagePayload,
) error {
	if err := q.turn.emitForRequest(
		ctx,
		EventTypeUserMessageCreated,
		payload,
		input.RequestID,
		input.SourceEventID,
	); err != nil {
		return ctxerrors.Wrap(err, "emit accepted queued user message")
	}

	if err := q.turn.emitForRequest(
		ctx,
		EventTypeUserMessageQueued,
		payload,
		input.RequestID,
		input.SourceEventID,
	); err != nil {
		return ctxerrors.Wrap(err, "emit queued user message")
	}

	return nil
}

func (q *activeUserMessageQueue) close() {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	q.closed = true
}

func (q *activeUserMessageQueue) checkpointDelivered(
	ctx context.Context,
	messages []elelem.Message,
) error {
	q.mutex.Lock()

	count := queuedUserMessageCountAtEnd(messages, q.pending)
	if count == 0 {
		q.mutex.Unlock()

		return nil
	}

	delivered := append([]queuedUserMessage(nil), q.pending[:count]...)
	q.pending = q.pending[count:]
	q.mutex.Unlock()

	inputs := make([]session.MessageInput, 0, len(delivered))
	for _, message := range delivered {
		inputs = append(inputs, session.MessageInput{
			Role:      models.MessageRoleUser,
			Content:   message.message,
			Workspace: q.workspace,
		})
	}

	if err := q.turn.appendQueuedUserMessages(ctx, inputs); err != nil {
		return ctxerrors.Wrap(err, "checkpoint delivered queued user messages")
	}

	for _, message := range delivered {
		if err := q.emitDelivered(ctx, message); err != nil {
			return err
		}
	}

	return nil
}

// emitDelivered reports one queued message reaching the model, under the
// request ID it was sent with, so every client can mark it as landed.
func (q *activeUserMessageQueue) emitDelivered(
	ctx context.Context,
	message queuedUserMessage,
) error {
	payload := userMessagePayload{Message: message.message}
	if message.sourceEventID != uuid.Nil {
		payload.SourceEventID = message.sourceEventID.String()
	}

	if err := q.turn.emitForRequest(
		ctx,
		EventTypeUserMessageDelivered,
		payload,
		message.requestID,
		message.sourceEventID,
	); err != nil {
		return ctxerrors.Wrap(err, "emit delivered queued user message")
	}

	return nil
}

func queuedUserMessageCountAtEnd(
	messages []elelem.Message,
	pending []queuedUserMessage,
) int {
	maximum := min(len(messages), len(pending))

	for count := maximum; count > 0; count-- {
		start := len(messages) - count
		matched := true

		for index := range count {
			if messages[start+index].Role != elelem.RoleUser ||
				messages[start+index].Text() != pending[index].message {
				matched = false

				break
			}
		}

		if matched {
			return count
		}
	}

	return 0
}

func (r *Runtime) registerActiveUserMessageQueue(
	prepared *preparedTurn,
) error {
	queue, err := newActiveUserMessageQueue(
		prepared,
		r.maxQueuedUserMessages,
	)
	if err != nil {
		return err
	}

	sessionID := prepared.opened.Session.ID

	r.userMessageQueuesMutex.Lock()
	defer r.userMessageQueuesMutex.Unlock()

	if _, exists := r.userMessageQueues[sessionID]; exists {
		return ctxerrors.Wrap(
			commerr.ErrInvalidState,
			"active user message queue",
		)
	}

	r.userMessageQueues[sessionID] = queue
	prepared.userMessages = queue

	return nil
}

func (r *Runtime) closeActiveUserMessageQueue(prepared *preparedTurn) {
	if prepared.userMessages == nil {
		return
	}

	prepared.userMessages.close()

	sessionID := prepared.opened.Session.ID

	r.userMessageQueuesMutex.Lock()
	defer r.userMessageQueuesMutex.Unlock()

	if r.userMessageQueues[sessionID] == prepared.userMessages {
		delete(r.userMessageQueues, sessionID)
	}
}

// queueActiveUserMessage accepts a plain caller message only when its target
// session has a queue registered by the still-running turn. The caller holds
// the session's admission gate, so no turn can start or register its queue
// between this lookup and the caller acting on its answer.
//
// Event-triggered turns never join a queue. One that finds a turn running is
// refused as busy here, before it would hold the gate through a whole turn
// preparation only to be refused by the lease.
func (r *Runtime) queueActiveUserMessage(
	ctx context.Context,
	input TurnRequest,
) (*TurnResult, bool, error) {
	// The queue is keyed by the session the request targets, not by the
	// runtime's own, because a control surface runs many sessions at once and
	// a message may only join the queue of the turn it belongs to.
	sessionID := r.turnSessionID(input)

	r.userMessageQueuesMutex.Lock()
	queue := r.userMessageQueues[sessionID]
	r.userMessageQueuesMutex.Unlock()

	if queue == nil {
		return nil, false, nil
	}

	if input.Origin != nil {
		return nil, true, ctxerrors.Wrap(
			session.ErrSessionBusy,
			"an event cannot start a turn while one runs",
		)
	}

	if queue.changesRunningTurn(input) {
		return nil, true, ctxerrors.Wrap(
			errors.Join(
				commerr.ErrValidationFailed,
				session.ErrRunningTurnSettingsChange,
			),
			"queue active user message",
		)
	}

	if err := queue.enqueue(ctx, input); err != nil {
		return nil, true, err
	}

	return &TurnResult{
		SessionID: sessionID,
		Queued:    true,
	}, true, nil
}

func (t *runtimeTurn) appendQueuedUserMessages(
	ctx context.Context,
	messages []session.MessageInput,
) error {
	for _, message := range messages {
		t.appendMessage(message)
	}

	return t.checkpoint(ctx)
}
