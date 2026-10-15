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

type rootPhaseDelivery struct {
	mutex     sync.Mutex
	projected bool
	delivered int
}

func (c *rootWorkflowCallbacks) afterToolPhase(
	ctx context.Context,
	input workflowToolPhaseInput,
	state workflowToolPhaseState,
) error {
	if state.RootProjection == nil {
		return nil
	}

	shared, release, err := c.acquire(ctx, input.Tool.Round.Execution, true)
	if err != nil {
		return ctxerrors.Wrap(err, "admit committed root phase")
	}
	defer release()

	if err := validateCommittedToolPhase(ctx, c.runtime, input, state,
		state.RootProjection.ActivityID); err != nil {
		return ctxerrors.Wrap(err, "validate committed root phase")
	}

	turn := shared.prepared.turn

	release()

	return shared.deliverRootPhaseProjection(ctx, turn, state.RootProjection)
}

func validateCommittedToolPhase(
	ctx context.Context,
	runtime *Runtime,
	input workflowToolPhaseInput,
	state workflowToolPhaseState,
	activityID uuid.UUID,
) error {
	store, ok := runtime.store.(session.ExecutionStorage)
	if !ok {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"committed root phase storage")
	}

	activity, err := store.GetExecutionActivity(ctx,
		input.Tool.Round.Execution.SessionID, activityID)
	if err != nil {
		return ctxerrors.Wrap(err, "read committed root phase")
	}

	encoded, err := json.Marshal(state)
	if err != nil {
		return ctxerrors.Wrap(err, "encode committed phase output")
	}

	encodedInput, err := json.Marshal(input)
	if err != nil {
		return ctxerrors.Wrap(err, "encode committed phase input")
	}

	if !sameRootPhaseProjection(activity, input, state,
		string(encodedInput), string(encoded)) {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"root phase output differs from committed activity")
	}

	return nil
}

func sameRootPhaseProjection(
	activity *models.ExecutionActivity,
	input workflowToolPhaseInput,
	state workflowToolPhaseState,
	encodedInput, encodedOutput string,
) bool {
	return activity.ExecutionID == input.Tool.Round.Execution.ExecutionID &&
		activity.State == models.ActivityStateCompleted &&
		activity.Name == toolPhaseActivityName && activity.OutputJSON != nil &&
		*activity.OutputJSON == encodedOutput &&
		activity.InputJSON == encodedInput &&
		state.LastPhase == input.Phase
}

func (s *rootCallbackScope) deliverRootPhaseProjection(
	ctx context.Context,
	turn *runtimeTurn,
	projection *rootToolProjection,
) error {
	s.phaseMutex.Lock()
	if s.phaseDeliveries == nil {
		s.phaseDeliveries = make(map[uuid.UUID]*rootPhaseDelivery)
	}

	delivery := s.phaseDeliveries[projection.ActivityID]
	if delivery == nil {
		delivery = &rootPhaseDelivery{}
		s.phaseDeliveries[projection.ActivityID] = delivery
	}
	s.phaseMutex.Unlock()

	delivery.mutex.Lock()
	defer delivery.mutex.Unlock()

	events := rootPhaseEvents(turn, projection.Transcript.Events)
	if !delivery.projected {
		turn.retainRootPhaseProjection(projection.Transcript, events)

		delivery.projected = true

		for _, event := range events {
			if turn.observer != nil {
				turn.observer(event)
			}
		}
	}

	for delivery.delivered < len(events) {
		if err := ctx.Err(); err != nil {
			return ctxerrors.Wrap(err, "deliver retained phase event")
		}

		turn.mutex.Lock()
		sink := turn.sink
		turn.mutex.Unlock()

		if sink != nil {
			if err := sink(events[delivery.delivered]); err != nil {
				return ctxerrors.Wrap(err, "deliver root phase event to sink")
			}
		}

		delivery.delivered++
	}

	return nil
}

func rootPhaseEvents(turn *runtimeTurn, inputs []session.EventInput) []Event {
	events := make([]Event, 0, len(inputs))
	for _, input := range inputs {
		events = append(events, Event{
			Type:              input.EventType,
			Payload:           json.RawMessage(input.PayloadJSON),
			RequestID:         input.RequestID,
			TriggeringEventID: turn.triggeringEventID,
		})
	}

	return events
}
