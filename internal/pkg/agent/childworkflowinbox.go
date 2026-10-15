package agent

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

type workflowChildInbox struct {
	mutex       sync.Mutex
	writer      session.ChildExecutionInboxStorage
	sessionID   uuid.UUID
	executionID uuid.UUID
	directory   string
	pending     map[uuid.UUID]int
	arrived     chan struct{}
	closed      bool
}

func newWorkflowChildInbox(
	ctx context.Context,
	store session.ExecutionStorage,
	writer session.ChildExecutionInboxStorage,
	sessionID, executionID, agentRunID uuid.UUID,
) (*workflowChildInbox, error) {
	if store == nil || writer == nil {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"durable child inbox storage")
	}

	execution, err := store.GetExecution(ctx, sessionID, executionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read child inbox owner")
	}

	if invalidChildCompactionExecution(execution) ||
		*execution.AgentRunID != agentRunID {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"durable inbox belongs to another child")
	}

	pending, err := readWorkflowChildPending(ctx, store, sessionID,
		executionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "restore queued child inbox identities")
	}

	return &workflowChildInbox{
		writer: writer, sessionID: sessionID, executionID: executionID,
		pending: pending, arrived: make(chan struct{}),
		directory: execution.WorkingDirectory,
	}, nil
}

func readWorkflowChildPending(
	ctx context.Context,
	store session.ExecutionStorage,
	sessionID, executionID uuid.UUID,
) (map[uuid.UUID]int, error) {
	pending := make(map[uuid.UUID]int)
	options := session.ListExecutionInboxOptions{
		State: models.InboxMessageStateQueued, Limit: session.DefaultPageLimit,
	}

	for {
		page, err := store.ListExecutionMessages(ctx, sessionID, executionID,
			options)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "read queued child inbox page")
		}

		for _, row := range page.Items {
			var parts []session.ExecutionInboxPart
			if err := json.Unmarshal([]byte(row.PartsJSON),
				&parts); err != nil {
				return nil, ctxerrors.Wrap(err,
					"decode queued child inbox slots")
			}

			pending[row.RequestID] = 1 + len(parts)
		}

		if !page.HasMore {
			return pending, nil
		}

		options.Offset += len(page.Items)
	}
}

func (i *agentInbox) bindWorkflow(
	ctx context.Context,
	executionID uuid.UUID,
	directory string,
) error {
	if i.sink == nil || i.sink.run == nil {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"child inbox sink and actor")
	}

	store, ok := i.sink.store.(session.ExecutionStorage)
	if !ok {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"child inbox read storage")
	}

	writer, ok := i.sink.store.(session.ChildExecutionInboxStorage)
	if !ok {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"child inbox write storage")
	}

	native, err := newWorkflowChildInbox(ctx, store, writer, i.sink.sessionID,
		executionID, i.sink.run.ID)
	if err != nil {
		return ctxerrors.Wrap(err, "bind durable child inbox")
	}

	i.mutex.Lock()
	defer i.mutex.Unlock()

	if i.closed || i.queue.Len() != 0 || i.workflow != nil {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"child inbox already contains live work")
	}

	native.directory = directory
	i.workflow = native

	return nil
}

func (i *agentInbox) markWorkflowDelivered(ids []uuid.UUID) {
	i.mutex.Lock()
	defer i.mutex.Unlock()

	if i.workflow != nil {
		i.workflow.markDelivered(ids)
	}
}

func (i *workflowChildInbox) deliverText(
	ctx context.Context,
	text string,
	capacity int,
) error {
	return i.accept(ctx, session.AcceptExecutionMessageInput{
		RequestID: uuid.New(),
		Message:   text, Capacity: capacity,
	})
}

func (i *workflowChildInbox) refreshDirectory(directory string) error {
	i.mutex.Lock()
	defer i.mutex.Unlock()

	if i.closed {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"durable child inbox is closed")
	}

	i.directory = directory

	return nil
}

func (i *workflowChildInbox) accept(
	ctx context.Context,
	input session.AcceptExecutionMessageInput,
) error {
	i.mutex.Lock()
	defer i.mutex.Unlock()

	if i.closed {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"durable child inbox is closed")
	}

	if input.WorkingDirectory == "" {
		input.WorkingDirectory = i.directory
	}

	accepted, err := i.writer.AcceptChildExecutionMessage(ctx, i.sessionID,
		i.executionID, input)
	if err != nil {
		return ctxerrors.Wrap(err, "accept durable child inbox input")
	}

	if _, exists := i.pending[input.RequestID]; exists ||
		accepted.Message.State != models.InboxMessageStateQueued {
		return nil
	}

	i.pending[input.RequestID] = 1 + len(input.Parts)
	close(i.arrived)
	i.arrived = make(chan struct{})

	return nil
}

func (i *workflowChildInbox) arrival() (int, <-chan struct{}) {
	i.mutex.Lock()
	defer i.mutex.Unlock()

	count := 0
	for _, slots := range i.pending {
		count += slots
	}

	return count, i.arrived
}

func (i *workflowChildInbox) markDelivered(ids []uuid.UUID) {
	i.mutex.Lock()
	defer i.mutex.Unlock()

	for _, id := range ids {
		delete(i.pending, id)
	}
}

func (i *workflowChildInbox) close() {
	i.mutex.Lock()
	defer i.mutex.Unlock()

	i.closed = true
}
