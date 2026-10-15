package agent

import (
	"context"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (c *rootWorkflowCallbacks) retirePaused(
	ctx context.Context,
	store session.ExecutionStorage,
	execution *models.Execution,
) error {
	c.mutex.Lock()
	slot := c.slots[execution.ID]
	c.mutex.Unlock()

	if slot == nil {
		return c.releasePausedLease(ctx, store, execution, nil)
	}

	release, err := slot.gate.acquire(ctx, true)
	if err != nil {
		return ctxerrors.Wrap(err, "admit paused root cleanup")
	}
	defer release()

	shared := slot.current
	if shared != nil {
		drain, err := shared.gate.acquire(ctx, true)
		if err != nil {
			return ctxerrors.Wrap(err, "drain paused root callbacks")
		}
		defer drain()
	}

	if err := c.releasePausedLease(ctx, store, execution, shared); err != nil {
		return ctxerrors.Wrap(err, "release paused root holder")
	}

	slot.current = nil

	shared.releaseTurnCapacity()

	return nil
}

func (c *rootWorkflowCallbacks) releasePausedLease(
	ctx context.Context,
	store session.ExecutionStorage,
	execution *models.Execution,
	shared *rootCallbackScope,
) error {
	current, err := store.GetExecution(ctx, execution.SessionID, execution.ID)
	if err != nil {
		return ctxerrors.Wrap(err, "recheck paused root state")
	}

	if current.State != models.ExecutionStatePaused {
		return ctxerrors.Wrap(commerr.ErrConflict, "root is no longer paused")
	}

	holder, err := store.GetExecutionTurnLease(ctx, execution.SessionID,
		execution.ID)
	if errors.Is(err, commerr.ErrNotFound) && shared == nil {
		return nil
	}

	if err != nil {
		return ctxerrors.Wrap(err, "read paused root holder")
	}

	// A replacement worker has no callbacks for the previous owner's lease.
	if shared == nil && holder.OwnerGenerationID != c.generationID {
		return nil
	}

	if c.pausedHolderChanged(holder, shared) {
		return ctxerrors.Wrap(commerr.ErrConflict, "paused root holder changed")
	}

	c.runtime.store.ReleaseTurn(session.Lease{
		ID: holder.LeaseID, SessionID: holder.SessionID, TurnID: holder.TurnID,
	})

	if shared != nil {
		c.runtime.closeActiveUserMessageQueue(shared.prepared)
		shared.prepared.turn.closeChildSink()
	}

	return nil
}

func (c *rootWorkflowCallbacks) pausedHolderChanged(
	holder *models.ExecutionTurnLease,
	shared *rootCallbackScope,
) bool {
	return holder.OwnerGenerationID != c.generationID ||
		(shared != nil && holder.LeaseID != shared.lease.ID)
}
