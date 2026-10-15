package agent

import (
	"context"
	"errors"
	"sync"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// agentInbox is a running child's message queue. A message lands in the
// child's conversation after its current tool call, or right after a final
// answer, which keeps the child going instead of ending it.
//
// The mutex orders acceptance against delivery and against the child ending,
// so every message is either delivered, reported back to its sender as
// refused, or recorded as undelivered.
type agentInbox struct {
	mutex    sync.Mutex
	queue    *elelem.UserMessageQueue
	sink     *agentRunSink
	capacity int
	closed   bool
	workflow *workflowChildInbox

	// pending holds every accepted message that has not yet been recorded as
	// delivered, oldest first.
	pending    []string
	pendingIDs []*uuid.UUID

	// arrived is closed and replaced each time a message is accepted, so a
	// waiter can block on whatever the current channel is.
	arrived chan struct{}
}

type agentInboxMessagePayload struct {
	Message      string     `json:"message"`
	AcceptanceID *uuid.UUID `json:"acceptanceId,omitempty"`
}

func newAgentInbox(sink *agentRunSink, capacity int) (*agentInbox, error) {
	queue, err := elelem.NewUserMessageQueue(capacity)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create agent inbox")
	}

	return &agentInbox{
		queue:    queue,
		sink:     sink,
		capacity: capacity,
		arrived:  make(chan struct{}),
	}, nil
}

// deliver accepts one rendered message. It fails with
// session.ErrAgentRunFinished once the child has ended, and with a conflict
// when the inbox is full.
func (i *agentInbox) deliver(ctx context.Context, text string) error {
	i.mutex.Lock()
	defer i.mutex.Unlock()

	if i.closed {
		return ctxerrors.Wrap(errAgentRunFinished(), "deliver agent message")
	}

	if i.workflow != nil {
		return i.workflow.deliverText(ctx, text, i.capacity)
	}

	if i.queue.Len() >= i.capacity {
		return ctxerrors.Wrap(
			errors.Join(commerr.ErrConflict, elelem.ErrUserMessageQueueFull),
			"deliver agent message",
		)
	}

	// The record comes first, so the child's event log never shows a
	// delivery whose acceptance is missing.
	acceptanceID := uuid.New()
	if err := i.sink.emit(
		ctx,
		EventTypeAgentRunMessageQueued,
		agentInboxMessagePayload{Message: text, AcceptanceID: &acceptanceID},
	); err != nil {
		return ctxerrors.Wrap(err, "record queued agent message")
	}

	if err := i.queue.EnqueueText(text); err != nil {
		return ctxerrors.Wrap(err, "enqueue agent message")
	}

	i.pending = append(i.pending, text)
	i.pendingIDs = append(i.pendingIDs, &acceptanceID)

	close(i.arrived)
	i.arrived = make(chan struct{})

	return nil
}

// checkpointDelivered records the messages a provider round just took from
// the queue: each becomes a durable user message in the child's transcript.
func (i *agentInbox) checkpointDelivered(
	ctx context.Context,
	messages []elelem.Message,
) error {
	i.mutex.Lock()
	defer i.mutex.Unlock()

	if i.workflow != nil {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"native child delivery belongs to the model activity")
	}

	count := deliveredInboxCount(messages, i.pending)
	if count == 0 {
		return nil
	}

	delivered := i.pending[:count]

	inputs := make([]session.AgentRunMessageInput, 0, len(delivered))
	for _, text := range delivered {
		inputs = append(inputs, session.AgentRunMessageInput{
			Role:    models.MessageRoleUser,
			Content: text,
		})
	}

	if err := i.sink.persistMessages(ctx, inputs...); err != nil {
		return ctxerrors.Wrap(err, "record delivered agent messages")
	}

	for index, text := range delivered {
		if err := i.sink.emit(
			ctx,
			EventTypeAgentRunMessageDelivered,
			agentInboxMessagePayload{
				Message: text, AcceptanceID: i.pendingID(index),
			},
		); err != nil {
			return ctxerrors.Wrap(err, "record delivered agent message")
		}
	}

	i.pending = i.pending[count:]
	if len(i.pendingIDs) >= count {
		i.pendingIDs = i.pendingIDs[count:]
	}

	return nil
}

func (i *agentInbox) pendingID(index int) *uuid.UUID {
	if index >= len(i.pendingIDs) {
		return nil
	}

	return i.pendingIDs[index]
}

func (i *agentInbox) unreadAcceptanceIDs() []*uuid.UUID {
	i.mutex.Lock()
	defer i.mutex.Unlock()

	unread := i.queue.Len()
	if unread > len(i.pendingIDs) {
		return nil
	}

	return append([]*uuid.UUID(nil), i.pendingIDs[len(i.pendingIDs)-unread:]...)
}

// arrival reports how many accepted messages no provider round has taken
// yet, and the channel that closes when the next one is accepted.
func (i *agentInbox) arrival() (int, <-chan struct{}) {
	i.mutex.Lock()
	defer i.mutex.Unlock()

	if i.workflow != nil {
		return i.workflow.arrival()
	}

	return i.queue.Len(), i.arrived
}

// close refuses every later message and returns the accepted ones the child
// never read.
func (i *agentInbox) close() []string {
	i.mutex.Lock()
	defer i.mutex.Unlock()

	i.closed = true

	if i.workflow != nil {
		i.workflow.close()

		return nil
	}

	unread := i.queue.Len()
	if unread == 0 {
		return nil
	}

	return append([]string(nil), i.pending[len(i.pending)-unread:]...)
}

// deliveredInboxCount reports how many of the first pending messages close
// the conversation a round is about to send.
func deliveredInboxCount(messages []elelem.Message, pending []string) int {
	for count := len(pending); count > 0; count-- {
		if count > len(messages) {
			continue
		}

		if endsWithUserTexts(messages, pending[:count]) {
			return count
		}
	}

	return 0
}
