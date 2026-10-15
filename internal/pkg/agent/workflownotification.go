package agent

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

//nolint:ireturn // Embedded and RPC stores share the execution contract.
func (d *workflowDispatch) notificationStore() (
	session.ExecutionStorage, error,
) {
	store, ok := d.runtime.store.(session.ExecutionStorage)
	if !ok {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"workflow notification storage")
	}

	return store, nil
}

func (d *workflowDispatch) notifyChildReport(
	ctx context.Context,
	input workflowExecutionInput,
) error {
	store, err := d.notificationStore()
	if err != nil {
		return ctxerrors.Wrap(err, "resolve child notification storage")
	}

	child, err := store.GetExecution(ctx, input.SessionID, input.ExecutionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read reporting child execution")
	}

	if child.ParentExecutionID == nil {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"reporting execution has no parent")
	}

	return d.notifyParent(ctx, child.SessionID, *child.ParentExecutionID)
}

func (d *workflowDispatch) notifyParent(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
) error {
	store, err := d.notificationStore()
	if err != nil {
		return ctxerrors.Wrap(err, "resolve parent notification storage")
	}

	parent, err := store.GetExecution(ctx, sessionID, executionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read report notification parent")
	}

	if childInboxUnavailable(parent.State) {
		return nil
	}

	switch parent.Kind {
	case models.ExecutionKindRoot:
		return d.notifyRoot(ctx, store, parent)
	case models.ExecutionKindChild:
		return d.notifyChild(ctx, store, parent)
	}

	return ctxerrors.Wrap(commerr.ErrInvalidState,
		"notification parent kind is unavailable")
}

func (d *workflowDispatch) notifyRoot(
	ctx context.Context,
	store session.ExecutionStorage,
	parent *models.Execution,
) error {
	d.root.mutex.Lock()
	slot := d.root.slots[parent.ID]
	d.root.mutex.Unlock()

	if slot == nil {
		return nil
	}

	release, err := slot.gate.acquire(ctx, true)
	if err != nil {
		return ctxerrors.Wrap(err, "admit root arrival notification")
	}
	defer release()

	shared := slot.current
	if shared == nil {
		return nil
	}

	reader, err := shared.gate.acquire(ctx, false)
	if err != nil {
		return ctxerrors.Wrap(err, "read cached root arrival scope")
	}
	defer reader()

	current, err := store.GetExecution(ctx, parent.SessionID, parent.ID)
	if err != nil {
		return ctxerrors.Wrap(err, "recheck root notification revision")
	}

	if childInboxUnavailable(current.State) ||
		current.ControlRevision != shared.revision {
		return nil
	}

	holder, err := store.GetExecutionTurnLease(ctx, parent.SessionID, parent.ID)
	if err != nil {
		return ctxerrors.Wrap(err, "read notification root holder")
	}

	if !matchingNotificationHolder(holder, shared, d.root.generationID) {
		return nil
	}

	return notifyRootWorkflowInbox(ctx, shared.prepared.userMessages, store)
}

func matchingNotificationHolder(
	holder *models.ExecutionTurnLease,
	shared *rootCallbackScope,
	generationID uuid.UUID,
) bool {
	return holder.OwnerGenerationID == generationID &&
		holder.LeaseID == shared.lease.ID &&
		holder.ControlRevision == shared.revision
}

func notifyRootWorkflowInbox(
	ctx context.Context,
	queue *activeUserMessageQueue,
	store session.ExecutionStorage,
) error {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()

	if queue.closed {
		return nil
	}

	pending, names, err := queue.readPendingWorkflowInbox(ctx, store,
		queue.executionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read notified root inbox")
	}

	hasNew := hasNewWorkflowArrivals(queue.workflowPending, pending)
	queue.workflowPending = pending
	queue.skills.record(names...)

	if hasNew {
		close(queue.arrived)
		queue.arrived = make(chan struct{})
	}

	return nil
}

func (d *workflowDispatch) notifyChild(
	ctx context.Context,
	store session.ExecutionStorage,
	parent *models.Execution,
) error {
	d.child.mutex.Lock()
	slot := d.child.slots[parent.ID]
	d.child.mutex.Unlock()

	if slot == nil {
		return nil
	}

	release, err := slot.gate.acquire(ctx, true)
	if err != nil {
		return ctxerrors.Wrap(err, "admit child arrival notification")
	}
	defer release()

	shared := slot.current
	if shared == nil {
		return nil
	}

	if shared.context.Err() != nil {
		return nil
	}

	reader, err := shared.gate.acquire(ctx, false)
	if err != nil {
		return ctxerrors.Wrap(err, "read cached child arrival scope")
	}
	defer reader()

	current, err := store.GetExecution(ctx, parent.SessionID, parent.ID)
	if err != nil {
		return ctxerrors.Wrap(err, "recheck child notification revision")
	}

	if childInboxUnavailable(current.State) ||
		current.ControlRevision != shared.revision {
		return nil
	}

	return notifyChildWorkflowInbox(ctx, shared.actor.run.inbox, store)
}

func notifyChildWorkflowInbox(
	ctx context.Context,
	inbox *agentInbox,
	store session.ExecutionStorage,
) error {
	inbox.mutex.Lock()
	defer inbox.mutex.Unlock()

	native := inbox.workflow
	native.mutex.Lock()
	defer native.mutex.Unlock()

	if inbox.closed || native.closed {
		return nil
	}

	pending, err := readWorkflowChildPending(ctx, store, native.sessionID,
		native.executionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read notified child inbox")
	}

	hasNew := hasNewWorkflowArrivals(native.pending, pending)
	native.pending = pending

	if hasNew {
		close(native.arrived)
		native.arrived = make(chan struct{})
	}

	return nil
}

func hasNewWorkflowArrivals(previous, next map[uuid.UUID]int) bool {
	for id := range next {
		if _, found := previous[id]; !found {
			return true
		}
	}

	return false
}
