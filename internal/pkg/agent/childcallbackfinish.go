package agent

import (
	"context"
	"encoding/json"
	"math"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (c *childWorkflowCallbacks) afterFinish(
	ctx context.Context,
	input workflowFinishInput,
	result workflowFinishResult,
) error {
	store, ok := c.runtime.store.(session.ExecutionStorage)
	if !ok {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"finished child execution storage")
	}

	execution, err := store.GetExecution(ctx, input.Execution.SessionID,
		input.Execution.ExecutionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read finished child execution")
	}

	if !finishedChildCallbackResult(execution, result) {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"child callback execution has not finished")
	}

	slot := c.slot(execution.ID)

	release, err := slot.gate.acquire(ctx, true)
	if err != nil {
		return ctxerrors.Wrap(err, "admit finished child cleanup")
	}
	defer release()

	return c.retireChildCallback(ctx, slot, execution)
}

func finishedChildCallbackResult(
	execution *models.Execution,
	result workflowFinishResult,
) bool {
	return !result.Continue && !invalidChildCompactionExecution(execution) &&
		result.Finish.Child != nil && execution.State == result.Finish.State &&
		string(result.Finish.Child.State) == string(execution.State) &&
		(execution.State == models.ExecutionStateCompleted ||
			execution.State == models.ExecutionStateFailed)
}

func (c *childWorkflowCallbacks) retireChildCallback(
	ctx context.Context,
	slot *childCallbackSlot,
	execution *models.Execution,
) error {
	shared := slot.current
	if shared != nil {
		if shared.revision != execution.ControlRevision &&
			execution.State != models.ExecutionStateCancelled {
			return ctxerrors.Wrap(commerr.ErrConflict,
				"finished child callback revision changed")
		}

		release, err := shared.gate.acquire(ctx, true)
		if err != nil {
			return ctxerrors.Wrap(err, "drain finished child callbacks")
		}
		defer release()
	}

	stored, event, err := c.readChildTerminalEvent(ctx, execution)
	if err != nil {
		return ctxerrors.Wrap(err, "read committed child terminal event")
	}

	if slot.terminalDelivered == event.ID {
		return nil
	}

	run, err := c.projectChildTerminalRun(ctx, shared, stored, event)
	if err != nil {
		return ctxerrors.Wrap(err, "project committed child state")
	}

	if shared != nil {
		shared.actor.run.inbox.close()
		shared.cancel()
	}

	if err := c.deliverChildTerminalEvent(run, event); err != nil {
		return ctxerrors.Wrap(err, "deliver committed child event")
	}

	slot.terminalDelivered, slot.current = event.ID, nil

	return nil
}

func (c *childWorkflowCallbacks) readChildTerminalEvent(
	ctx context.Context,
	execution *models.Execution,
) (*models.AgentRun, *models.AgentRunEvent, error) {
	run, err := c.runtime.store.GetAgentRun(ctx, execution.SessionID,
		*execution.AgentRunID)
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "read committed child outcome")
	}

	if run.EndedAt == nil || run.EventCount < 1 {
		return nil, nil, ctxerrors.Wrap(commerr.ErrInvalidState,
			"committed child terminal record is missing")
	}

	kind := map[models.ExecutionState]string{
		models.ExecutionStateCompleted: EventTypeAgentRunCompleted,
		models.ExecutionStateFailed:    EventTypeAgentRunFailed,
		models.ExecutionStateCancelled: EventTypeAgentRunCancelled,
	}[execution.State]

	if string(run.State) != string(execution.State) {
		return nil, nil, ctxerrors.Wrap(commerr.ErrConflict,
			"committed child and execution outcomes differ")
	}

	page, err := c.runtime.store.ListAgentRunEvents(ctx, execution.SessionID,
		run.ID, session.ListAgentRunEventsOptions{
			Cursor: run.EventCount - 1, Limit: 1,
		})
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "read retained child final event")
	}

	if len(page.Items) != 1 || page.Items[0].EventType != kind ||
		page.Items[0].Sequence > math.MaxInt {
		return nil, nil, ctxerrors.Wrap(commerr.ErrConflict,
			"retained child terminal event differs")
	}

	return run, page.Items[0], nil
}

func (c *childWorkflowCallbacks) projectChildTerminalRun(
	ctx context.Context,
	shared *childCallbackScope,
	stored *models.AgentRun,
	event *models.AgentRunEvent,
) (*AgentRun, error) {
	if err := ctx.Err(); err != nil {
		return nil, ctxerrors.Wrap(err, "project terminal child registry")
	}

	registry, err := c.runtime.sessionAgentRuns(stored.SessionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read terminal child registry")
	}

	run, found := registry.Get(stored.ID)
	if shared == nil && !found {
		run, err = registry.restoreWorkflowRun(stored, func() {}, nil)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "restore terminal child registry")
		}

		run.AppendStoredEvent(retainedChildRegistryEvent(event))

		return run, nil
	}

	if shared != nil && run != shared.actor.run {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"terminal child registry owner changed")
	}

	identity := run.Snapshot()

	identity.State = string(stored.State)
	if !sameWorkflowRegistryRun(identity, stored) {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"terminal child registry identity changed")
	}

	run.mu.Lock()
	if run.state == AgentRunStateRunning {
		run.state, run.endedAt = string(stored.State), *stored.EndedAt
		close(run.done)
	}
	run.mu.Unlock()
	run.AppendStoredEvent(retainedChildRegistryEvent(event))

	return run, nil
}

func retainedChildRegistryEvent(event *models.AgentRunEvent) AgentRunEvent {
	return AgentRunEvent{
		Sequence: int(event.Sequence), Type: event.EventType,
		Payload: json.RawMessage(event.PayloadJSON), CreatedAt: event.CreatedAt,
	}
}

func (c *childWorkflowCallbacks) deliverChildTerminalEvent(
	run *AgentRun,
	event *models.AgentRunEvent,
) error {
	if c.sink == nil {
		return nil
	}

	payload, err := json.Marshal(agentRunLivePayload{
		AgentRunID: run.ID, ParentTurnID: run.ParentTurnID,
		ParentAgentRunID: run.ParentAgentRunID,
		Event:            json.RawMessage(event.PayloadJSON),
	})
	if err != nil {
		return ctxerrors.Wrap(err, "encode retained child terminal event")
	}

	if err := c.sink(Event{
		Type:    event.EventType,
		Payload: payload,
	}); err != nil {
		return ctxerrors.Wrap(err, "publish retained child terminal event")
	}

	return nil
}
