package agent

import (
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

// The final callback holds the scope gate while projecting a committed result.
func projectRootFinalEvent(
	ctx context.Context,
	shared *rootCallbackScope,
	result workflowFinishResult,
) error {
	if result.Finish.Turn == nil || shared.finalDelivered {
		return nil
	}

	event, err := retainedRootFinalEvent(shared.prepared, result)
	if err != nil {
		return ctxerrors.Wrap(err, "decode committed root final event")
	}

	turn := shared.prepared.turn
	turn.store.ReleaseTurn(shared.lease)

	if !shared.finalProjected {
		turn.retainRootFinalEvent(event, result.Finish.Turn.State)

		shared.finalProjected = true

		if turn.observer != nil {
			turn.observer(event)
		}
	}

	if err := ctx.Err(); err != nil {
		return ctxerrors.Wrap(err, "deliver committed root final event")
	}

	turn.mutex.Lock()
	sink := turn.sink
	turn.mutex.Unlock()

	if sink != nil {
		if err := sink(event); err != nil {
			return ctxerrors.Wrap(err, "deliver root final event to sink")
		}
	}

	shared.finalDelivered = true

	return nil
}

func retainedRootFinalEvent(
	prepared *preparedTurn,
	result workflowFinishResult,
) (Event, error) {
	inputs := result.Finish.Turn.Events
	if len(inputs) == 0 {
		return Event{}, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"committed root final event is missing")
	}

	input := inputs[len(inputs)-1]

	kind := EventTypeTurnCompleted
	if result.Finish.State == models.ExecutionStateFailed {
		kind = EventTypeTurnFailed
	}

	if input.EventType != kind || input.RequestID != prepared.turn.requestID ||
		!json.Valid([]byte(input.PayloadJSON)) {
		return Event{}, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"committed root final event differs")
	}

	return Event{
		Type: kind, Payload: json.RawMessage(input.PayloadJSON),
		RequestID:         input.RequestID,
		TriggeringEventID: prepared.turn.triggeringEventID,
	}, nil
}

func (t *runtimeTurn) retainRootFinalEvent(
	event Event,
	state models.TurnState,
) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.events = append(t.events, event)
	if state != models.TurnStateCompleted {
		for index := range t.messages {
			if t.messages[index].Role != models.MessageRoleUser {
				t.messages[index].Incomplete = true
			}
		}
	}

	t.checkpointedMessages = len(t.messages)
	t.checkpointedEvents = len(t.events)
}
