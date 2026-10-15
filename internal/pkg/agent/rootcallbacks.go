package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

type rootWorkflowCallbacks struct {
	runtime      *Runtime
	generationID uuid.UUID
	sink         EventSink
	mutex        sync.Mutex
	slots        map[uuid.UUID]*rootCallbackSlot
}

type rootCallbackSlot struct {
	gate    rootCallbackGate
	current *rootCallbackScope
}

type rootCallbackScope struct {
	releaseTurnSlot func()
	gate            rootCallbackGate
	prepared        *preparedTurn
	directory       *directoryScope
	revision        int64
	lease           session.Lease
	finalProjected  bool
	finalDelivered  bool
	phaseDeliveries map[uuid.UUID]*rootPhaseDelivery
	phaseMutex      sync.Mutex
}

func newRootWorkflowCallbacks(
	runtime *Runtime,
	generationID uuid.UUID,
	sink EventSink,
) *rootWorkflowCallbacks {
	return &rootWorkflowCallbacks{
		runtime: runtime, generationID: generationID, sink: sink,
		slots: make(map[uuid.UUID]*rootCallbackSlot),
	}
}

func (c *rootWorkflowCallbacks) bind(options *workflowExecutionOptions) {
	options.OpeningHook = c.openingHook
	options.AfterOpeningHook = c.afterOpeningHook
	options.PrepareRound = c.prepareRound
	options.CompactRound = c.compactRound
	options.CompactionHook = c.compactionHook
	options.ModelRound = c.modelRound
	options.AfterModelRound = c.afterModelRound
	options.ToolCall = c.toolCall
	options.ToolPhase = c.toolPhase
	options.AfterToolPhase = c.afterToolPhase
	options.AfterFinish = c.afterFinish
	options.BeforeFinish = c.beforeFinish
	options.TerminalHooks = c.terminalHooks
}

func (c *rootWorkflowCallbacks) slot(id uuid.UUID) *rootCallbackSlot {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.slots[id] == nil {
		c.slots[id] = &rootCallbackSlot{}
	}

	return c.slots[id]
}

func (c *rootWorkflowCallbacks) resolve(
	ctx context.Context,
	input workflowExecutionInput,
) (*rootCallbackScope, error) {
	store, ok := c.runtime.store.(session.ExecutionStorage)
	if !ok {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"root callback storage")
	}

	execution, err := c.readState(ctx, store, input)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "validate root callback state")
	}

	slot := c.slot(input.ExecutionID)

	release, err := slot.gate.acquire(ctx, true)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "admit root scope construction")
	}
	defer release()

	holder, err := store.GetExecutionTurnLease(ctx, input.SessionID,
		input.ExecutionID)
	if err != nil && !errors.Is(err, commerr.ErrNotFound) {
		return nil, ctxerrors.Wrap(err, "read root callback holder")
	}

	canReuse, err := c.reusable(slot.current, holder, execution)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "check shared root holder")
	}

	if canReuse {
		return slot.current, nil
	}

	return c.construct(ctx, store, slot, input, holder)
}

func (c *rootWorkflowCallbacks) readState(
	ctx context.Context,
	store session.ExecutionStorage,
	input workflowExecutionInput,
) (*models.Execution, error) {
	conversation, original, err := c.runtime.readRootWorkflowBootstrap(ctx,
		store, input)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "validate root callback identity")
	}

	if err := c.runtime.validateRootBootstrap(conversation.Root,
		original); err != nil {
		return nil, ctxerrors.Wrap(err, "validate root callback bootstrap")
	}

	execution, err := store.GetExecution(ctx, input.SessionID,
		input.ExecutionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read root callback state")
	}

	if execution.State == models.ExecutionStatePaused ||
		execution.State == models.ExecutionStateCompleted ||
		execution.State == models.ExecutionStateFailed {
		return nil, ctxerrors.Wrap(commerr.ErrInvalidState,
			"root callback execution is unavailable")
	}

	return execution, nil
}

func (c *rootWorkflowCallbacks) reusable(
	shared *rootCallbackScope,
	holder *models.ExecutionTurnLease,
	execution *models.Execution,
) (bool, error) {
	if shared == nil || holder == nil {
		return false, nil
	}

	if holder.OwnerGenerationID != c.generationID {
		return false, ctxerrors.Wrap(commerr.ErrConflict,
			"root callback holder changed")
	}

	if holder.ControlRevision != shared.revision ||
		execution.ControlRevision != shared.revision {
		return false, nil
	}

	if holder.LeaseID != shared.lease.ID {
		return false, ctxerrors.Wrap(commerr.ErrConflict,
			"root callback lease changed")
	}

	return true, nil
}

func (c *rootWorkflowCallbacks) construct(
	ctx context.Context,
	store session.ExecutionStorage,
	slot *rootCallbackSlot,
	input workflowExecutionInput,
	holder *models.ExecutionTurnLease,
) (*rootCallbackScope, error) {
	var releaseSlot func()
	if slot.current != nil {
		releaseSlot = slot.current.releaseTurnSlot
	}

	isNew := releaseSlot == nil
	if isNew {
		release, err := c.runtime.acquireTurnSlot(ctx)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "admit restored root turn")
		}

		releaseSlot = sync.OnceFunc(release)
	}

	shared, err := c.restoreCallbackScope(ctx, store, slot, input, holder)
	if err != nil {
		if isNew {
			releaseSlot()
		}

		return nil, ctxerrors.Wrap(err, "restore admitted root scope")
	}

	shared.releaseTurnSlot = releaseSlot

	return shared, nil
}

func (c *rootWorkflowCallbacks) restoreCallbackScope(
	ctx context.Context,
	store session.ExecutionStorage,
	slot *rootCallbackSlot,
	input workflowExecutionInput,
	holder *models.ExecutionTurnLease,
) (*rootCallbackScope, error) {
	execution, err := store.GetExecution(ctx, input.SessionID,
		input.ExecutionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read root callback revision")
	}

	claim := session.RestoreExecutionTurnInput{
		OwnerGenerationID:  c.generationID,
		ControlRevision:    execution.ControlRevision,
		WorkerGenerationID: c.runtime.workerGenerationID,
	}
	if holder != nil {
		claim.PreviousLeaseID = holder.LeaseID
	}

	prepared, directory, err := c.runtime.restoreRootWorkflowTurn(ctx, input,
		claim, c.sink)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "construct shared root callbacks")
	}

	slot.current = &rootCallbackScope{
		prepared: prepared, directory: directory,
		revision: claim.ControlRevision,
		lease:    prepared.lease,
	}

	return slot.current, nil
}

func (s *rootCallbackScope) releaseTurnCapacity() {
	if s != nil && s.releaseTurnSlot != nil {
		s.releaseTurnSlot()
	}
}

func (c *rootWorkflowCallbacks) prepareRound(
	ctx context.Context,
	input workflowRoundInput,
) (json.RawMessage, error) {
	shared, release, err := c.acquire(ctx, input.Execution, true)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "acquire root preparation callbacks")
	}
	defer release()

	if err := c.refresh(ctx, shared, input.Execution); err != nil {
		return nil, ctxerrors.Wrap(err, "refresh root preparation callbacks")
	}

	return c.runtime.prepareRootWorkflowRound(ctx, shared.prepared,
		shared.directory, input)
}

func (c *rootWorkflowCallbacks) modelRound(
	ctx context.Context,
	input workflowRoundInput,
) (workflowRoundResult, error) {
	shared, release, err := c.acquire(ctx, input.Execution, true)
	if err != nil {
		return workflowRoundResult{}, ctxerrors.Wrap(err,
			"acquire root model callbacks")
	}
	defer release()

	if err := c.refresh(ctx, shared, input.Execution); err != nil {
		return workflowRoundResult{}, ctxerrors.Wrap(err,
			"refresh root model callbacks")
	}

	assistant, activityID, err := c.rootModelProjectionTurn(ctx, shared, input)
	if err != nil {
		return workflowRoundResult{}, ctxerrors.Wrap(err,
			"buffer root model transcript")
	}

	result, err := c.runtime.workflowRootModelRoundWithAssistant(ctx,
		shared.prepared, assistant, input)
	if err != nil {
		return result, ctxerrors.Wrap(err, "run root model callbacks")
	}

	if activityID != uuid.Nil {
		result.RootProjection = assistant.rootToolProjection(shared, activityID)
	}

	return result, nil
}

func (c *rootWorkflowCallbacks) toolCall(
	ctx context.Context,
	input workflowToolInput,
) (workflowToolResult, error) {
	shared, release, err := c.acquire(ctx, input.Round.Execution, false)
	if err != nil {
		return workflowToolResult{}, ctxerrors.Wrap(err,
			"acquire root tool callbacks")
	}
	defer release()

	return c.runtime.workflowRootToolCall(ctx, shared.prepared,
		shared.directory, input)
}
