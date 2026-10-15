package agent

import (
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (c *rootWorkflowCallbacks) beforeFinish(
	ctx context.Context,
	input workflowFinishInput,
) (workflowFinishResult, error) {
	shared, release, err := c.acquire(ctx, input.Execution, true)
	if err != nil {
		return workflowFinishResult{}, ctxerrors.Wrap(err,
			"acquire final root callback holder")
	}
	defer release()

	result := input.Result

	result.Finish.RootHolder = &session.ExecutionFinishHolder{
		LeaseID: shared.lease.ID, ControlRevision: shared.revision,
	}
	if result.Finish.WorkingDirectory == "" {
		result.Finish.WorkingDirectory = shared.prepared.workspace
	}

	pending := shared.prepared.turn.pendingTranscript()

	state := models.TurnStateCompleted
	classification := ""

	if result.Finish.State == models.ExecutionStateFailed {
		state = models.TurnStateFailed
		classification = failureClassAgentRun
	}

	result.Finish.Turn = &session.FinalizeTurnInput{
		State: state, FailureClassification: classification,
		ContextSnapshotHash: &shared.prepared.contextHash,
		PromptSnapshotHash:  &shared.prepared.promptHash,
		Messages:            pending.messages, Events: pending.events,
	}

	finalEvent, err := c.finalEvent(shared.prepared, result)
	if err != nil {
		return workflowFinishResult{}, ctxerrors.Wrap(err,
			"prepare final root event")
	}

	result.Finish.Turn.Events = append(result.Finish.Turn.Events, finalEvent)

	return result, nil
}

func (c *rootWorkflowCallbacks) finalEvent(
	prepared *preparedTurn,
	result workflowFinishResult,
) (session.EventInput, error) {
	kind := EventTypeTurnCompleted

	var payload any = turnCompletedPayload{
		Model: prepared.modelReference, Text: result.Result.Text,
	}
	if result.Finish.State == models.ExecutionStateFailed {
		kind = EventTypeTurnFailed
		payload = turnFailedPayload{Reason: failureClassAgentRun}
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return session.EventInput{}, ctxerrors.Wrap(err,
			"encode final root event")
	}

	return session.EventInput{
		RequestID:          prepared.turn.requestID,
		WorkerGenerationID: c.generationID.String(),
		EventType:          kind, PayloadJSON: string(encoded),
	}, nil
}

func finishedRootCallbackResult(
	execution *models.Execution,
	result workflowFinishResult,
) bool {
	return !result.Continue && execution.Kind == models.ExecutionKindRoot &&
		(execution.State == models.ExecutionStateCompleted ||
			execution.State == models.ExecutionStateFailed) &&
		execution.State == result.Finish.State
}

func (c *rootWorkflowCallbacks) afterFinish(
	ctx context.Context,
	input workflowFinishInput,
	result workflowFinishResult,
) error {
	if input.Execution.SessionID != c.runtime.sessionID {
		return ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"finished callback session differs")
	}

	store, ok := c.runtime.store.(session.ExecutionStorage)
	if !ok {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"finished root callback storage")
	}

	execution, err := store.GetExecution(ctx, input.Execution.SessionID,
		input.Execution.ExecutionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read finished callback execution")
	}

	if !finishedRootCallbackResult(execution, result) {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"root callback execution has not finished")
	}

	c.mutex.Lock()
	slot := c.slots[execution.ID]
	c.mutex.Unlock()

	if slot == nil {
		return nil
	}

	release, err := slot.gate.acquire(ctx, true)
	if err != nil {
		return ctxerrors.Wrap(err, "admit finished callback cleanup")
	}
	defer release()

	return c.retireFinishedScope(ctx, store, slot, execution, result)
}

func (c *rootWorkflowCallbacks) retireFinishedScope(
	ctx context.Context,
	store session.ExecutionStorage,
	slot *rootCallbackSlot,
	execution *models.Execution,
	result workflowFinishResult,
) error {
	shared := slot.current
	if shared == nil {
		return nil
	}

	release, err := shared.gate.acquire(ctx, true)
	if err != nil {
		return ctxerrors.Wrap(err, "drain finished root callbacks")
	}
	defer release()

	holder, err := store.GetExecutionTurnLease(ctx, execution.SessionID,
		execution.ID)
	if err != nil {
		return ctxerrors.Wrap(err, "verify finished root callback holder")
	}

	if holder.LeaseID != shared.lease.ID ||
		holder.OwnerGenerationID != c.generationID ||
		holder.ControlRevision != shared.revision ||
		execution.ControlRevision != shared.revision {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"finished root callback holder changed")
	}

	c.runtime.closeActiveUserMessageQueue(shared.prepared)
	shared.prepared.turn.closeChildSink()
	shared.releaseTurnCapacity()

	if err := projectRootFinalEvent(ctx, shared, result); err != nil {
		return ctxerrors.Wrap(err, "project committed root final event")
	}

	slot.current = nil

	c.mutex.Lock()
	if c.slots[execution.ID] == slot {
		delete(c.slots, execution.ID)
	}
	c.mutex.Unlock()
	ctxscope.GetLogger(ctx).Info("root workflow callbacks retired",
		"session_id", execution.SessionID, "execution_id", execution.ID)

	return nil
}
