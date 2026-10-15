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

type childWorkflowCallbacks struct {
	runtime      *Runtime
	generationID uuid.UUID
	//nolint:containedctx // Callback lifetimes belong to this worker.
	lifecycle context.Context
	sink      EventSink
	mutex     sync.Mutex
	slots     map[uuid.UUID]*childCallbackSlot
}

type childCallbackSlot struct {
	gate              rootCallbackGate
	current           *childCallbackScope
	terminalDelivered uuid.UUID
}

type childCallbackScope struct {
	gate            rootCallbackGate
	actor           *childWorkflowActor
	revision        int64
	phaseDeliveries map[uuid.UUID]int
	//nolint:containedctx // Registry cancellation must reach active callbacks.
	context context.Context
	cancel  context.CancelFunc
}

func newChildWorkflowCallbacks(
	lifecycle context.Context,
	runtime *Runtime,
	sink EventSink,
) *childWorkflowCallbacks {
	if observer := runtime.sessionObserver(runtime.sessionID); sink == nil &&
		observer != nil {
		sink = func(event Event) error {
			observer(event)

			return nil
		}
	}

	return &childWorkflowCallbacks{
		runtime: runtime, lifecycle: lifecycle, sink: sink,
		slots: make(map[uuid.UUID]*childCallbackSlot),
	}
}

func (c *childWorkflowCallbacks) slot(id uuid.UUID) *childCallbackSlot {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.slots[id] == nil {
		c.slots[id] = &childCallbackSlot{}
	}

	return c.slots[id]
}

func (c *childWorkflowCallbacks) resolve(
	ctx context.Context,
	input workflowRoundInput,
) (*childCallbackScope, error) {
	slot := c.slot(input.Execution.ExecutionID)

	release, err := slot.gate.acquire(ctx, true)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "admit child callback construction")
	}
	defer release()

	revision, err := c.readRevision(ctx, input)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read child callback revision")
	}

	if slot.current != nil && slot.current.revision == revision {
		return slot.current, nil
	}

	return c.construct(ctx, slot, input, revision)
}

func (c *childWorkflowCallbacks) readRevision(
	ctx context.Context,
	input workflowRoundInput,
) (int64, error) {
	var conversation workflowConversation
	if err := json.Unmarshal(input.Execution.Context,
		&conversation); err != nil {
		return 0, ctxerrors.Wrap(err, "decode shared child context")
	}

	if err := c.runtime.validateChildWorkflowInbox(ctx, input,
		&conversation); err != nil {
		return 0, ctxerrors.Wrap(err, "validate shared child identity")
	}

	store, ok := c.runtime.store.(session.ExecutionStorage)
	if !ok {
		return 0, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"shared child execution storage")
	}

	execution, err := store.GetExecution(ctx, input.Execution.SessionID,
		input.Execution.ExecutionID)
	if err != nil {
		return 0, ctxerrors.Wrap(err, "read shared child execution")
	}

	if childInboxUnavailable(execution.State) {
		return 0, ctxerrors.Wrap(commerr.ErrInvalidState,
			"shared child execution is unavailable")
	}

	return execution.ControlRevision, nil
}

func (c *childWorkflowCallbacks) construct(
	ctx context.Context,
	slot *childCallbackSlot,
	input workflowRoundInput,
	revision int64,
) (*childCallbackScope, error) {
	release, err := drainChildCallbackScope(ctx, slot.current)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "drain previous child callbacks")
	}
	defer release()

	lifetime, cancel := context.WithCancel(c.lifecycle)
	c.runtime.childMessageMutex.Lock()
	defer c.runtime.childMessageMutex.Unlock()

	actor, err := c.runtime.restoreChildWorkflowActor(ctx, input,
		cancel, c.sink)
	if err != nil {
		cancel()

		return nil, ctxerrors.Wrap(err, "construct shared child actor")
	}

	current, err := c.readRevision(ctx, input)
	if err != nil || current != revision {
		cancel()

		if err != nil {
			return nil, ctxerrors.Wrap(err,
				"recheck constructed child revision")
		}

		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"child revision changed during construction")
	}

	registry, err := c.runtime.sessionAgentRuns(input.Execution.SessionID)
	if err != nil {
		cancel()

		return nil, ctxerrors.Wrap(err, "resolve child callback registry")
	}

	if err := installChildCallbackActor(registry, slot.current,
		actor); err != nil {
		cancel()

		return nil, ctxerrors.Wrap(err, "install child callback actor")
	}

	if slot.current != nil {
		slot.current.cancel()
		slot.current.actor.run.inbox.close()
	}

	slot.current = &childCallbackScope{
		actor: actor, revision: revision, context: lifetime, cancel: cancel,
	}

	return slot.current, nil
}

func installChildCallbackActor(
	registry *AgentRunRegistry,
	previous *childCallbackScope,
	actor *childWorkflowActor,
) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()

	existing := registry.runs[actor.run.ID]
	if existing != nil &&
		(previous == nil || existing != previous.actor.run) {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"child registry actor belongs to another callback owner")
	}

	running := registry.runningCountLocked()
	if existing != nil && !existing.isPaused.Load() &&
		existing.Snapshot().State == AgentRunStateRunning {
		running--
	}

	if running >= registry.limits.MaxConcurrentRuns {
		return ctxerrors.Wrap(ErrTooManyAgentRuns,
			"restored child exceeds the active run limit")
	}

	registry.runs[actor.run.ID] = actor.run

	return nil
}

func (c *childWorkflowCallbacks) pause(
	ctx context.Context,
	execution *models.Execution,
) error {
	slot := c.slot(execution.ID)

	release, err := slot.gate.acquire(ctx, true)
	if err != nil {
		return ctxerrors.Wrap(err, "admit paused child cleanup")
	}
	defer release()

	drained, err := drainChildCallbackScope(ctx, slot.current)
	if err != nil {
		return ctxerrors.Wrap(err, "drain paused child callbacks")
	}
	defer drained()

	registry := c.runtime.existingAgentRuns(execution.SessionID)
	if registry == nil || execution.AgentRunID == nil {
		return nil
	}

	run, found := registry.Get(*execution.AgentRunID)
	if found {
		registry.Cancel(run.ID)
		run.isPaused.Store(true)
	}

	return nil
}
