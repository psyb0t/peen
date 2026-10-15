package agent

import (
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (c *rootWorkflowCallbacks) acquire(
	ctx context.Context,
	input workflowExecutionInput,
	exclusive bool,
) (*rootCallbackScope, func(), error) {
	shared, err := c.resolve(ctx, input)
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "resolve root callback scope")
	}

	release, err := shared.gate.acquire(ctx, exclusive)
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "lock root callback scope")
	}

	store, ok := c.runtime.store.(session.ExecutionStorage)
	if !ok {
		release()

		return nil, nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"admitted root callback storage")
	}

	execution, err := c.readState(ctx, store, input)
	if err != nil {
		release()

		return nil, nil, ctxerrors.Wrap(err, "recheck admitted root state")
	}

	holder, err := store.GetExecutionTurnLease(ctx, input.SessionID,
		input.ExecutionID)
	if err != nil {
		release()

		return nil, nil, ctxerrors.Wrap(err, "recheck admitted root holder")
	}

	if holder.LeaseID != shared.lease.ID ||
		holder.OwnerGenerationID != c.generationID ||
		holder.ControlRevision != shared.revision ||
		execution.ControlRevision != shared.revision {
		release()

		return nil, nil, ctxerrors.Wrap(commerr.ErrConflict,
			"admitted root callback holder changed")
	}

	return shared, release, nil
}

func (c *rootWorkflowCallbacks) refresh(
	ctx context.Context,
	shared *rootCallbackScope,
	input workflowExecutionInput,
) error {
	var conversation workflowConversation
	if err := json.Unmarshal(input.Context, &conversation); err != nil {
		return ctxerrors.Wrap(err, "decode shared root context")
	}

	seed := *shared.prepared
	seed.workflowInbox = false

	restored, err := c.runtime.restoreRootWorkflowState(ctx, &seed,
		conversation)
	if err != nil {
		return ctxerrors.Wrap(err, "restore shared root state")
	}

	previous := *shared.prepared
	*shared.prepared = *restored

	directory, err := c.runtime.rootDirectoryScope(ctx, shared.prepared)
	if err != nil {
		*shared.prepared = previous

		return ctxerrors.Wrap(err, "bind refreshed root scope")
	}

	current := directory.context()
	current.contextHash = restored.contextHash

	current.promptHash = restored.promptHash
	if err := c.refreshQueue(ctx, shared.prepared.userMessages,
		current, input); err != nil {
		*shared.prepared = previous

		return ctxerrors.Wrap(err, "publish refreshed root queue context")
	}

	shared.prepared.workflowInbox = true
	shared.directory = directory
	directory.applyContext(current)

	return nil
}

func (c *rootWorkflowCallbacks) refreshQueue(
	ctx context.Context,
	queue *activeUserMessageQueue,
	current *directoryContext,
	input workflowExecutionInput,
) error {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()

	store, ok := c.runtime.store.(session.ExecutionStorage)
	if !ok {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"refreshed root callback storage")
	}

	pending, names, err := queue.readPendingWorkflowInbox(ctx, store,
		input.ExecutionID)
	if err != nil {
		return ctxerrors.Wrap(err, "restore refreshed root arrivals")
	}

	current.skills.record(names...)
	queue.snapshot = current.snapshot
	queue.skills = current.skills
	queue.workspace = current.executor.Workspace()
	queue.workflowPending = pending

	return nil
}
