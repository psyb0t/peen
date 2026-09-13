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
}

type queuedUserMessage struct {
	message string
}

type userMessagePayload struct {
	Message       string `json:"message"`
	SourceEventID string `json:"sourceEventId,omitempty"`
}

func newActiveUserMessageQueue(
	turn *runtimeTurn,
	workspace string,
	capacity int,
) (*activeUserMessageQueue, error) {
	queue, err := elelem.NewUserMessageQueue(capacity)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create active user message queue")
	}

	return &activeUserMessageQueue{
		queue:     queue,
		turn:      turn,
		workspace: workspace,
		capacity:  capacity,
	}, nil
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
		message: input.Message,
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
		prepared.turn,
		prepared.workspace,
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
// session has a queue registered by the still-running turn. Event-triggered
// turns remain on their isolated event path.
func (r *Runtime) queueActiveUserMessage(
	ctx context.Context,
	input TurnRequest,
) (*TurnResult, bool, error) {
	if input.SessionID == nil || input.Origin != nil {
		return nil, false, nil
	}

	r.userMessageQueuesMutex.Lock()
	queue := r.userMessageQueues[*input.SessionID]
	r.userMessageQueuesMutex.Unlock()

	if queue == nil {
		return nil, false, nil
	}

	if input.Workspace != "" || input.Model != "" ||
		input.SystemPrompt != "" || input.SystemPromptMode != "" {
		return nil, true, ctxerrors.Wrap(
			commerr.ErrConflict,
			"active turn cannot change workspace, model, or system prompt",
		)
	}

	if err := queue.enqueue(ctx, input); err != nil {
		return nil, true, err
	}

	return &TurnResult{
		SessionID: *input.SessionID,
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
