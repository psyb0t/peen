package agent

import (
	"slices"
	"strconv"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/session"
)

type rootToolProjection struct {
	ActivityID uuid.UUID                       `json:"activityId"`
	Transcript session.ExecutionRootProjection `json:"transcript"`
}

func newRootPhaseTurn(original *runtimeTurn) *runtimeTurn {
	return &runtimeTurn{
		requestID:          original.requestID,
		triggeringEventID:  original.triggeringEventID,
		workerGenerationID: original.workerGenerationID,
		lease:              original.lease,
	}
}

func (p *preparedTurn) rootToolProjection(
	shared *rootCallbackScope,
	activityID uuid.UUID,
) *rootToolProjection {
	pending := p.turn.pendingTranscript()
	for index := range pending.events {
		pending.events[index].ID = uuid.NewSHA1(activityID,
			[]byte(strconv.Itoa(index)))
	}

	return &rootToolProjection{
		ActivityID: activityID,
		Transcript: session.ExecutionRootProjection{
			Holder: session.ExecutionFinishHolder{
				LeaseID:         shared.lease.ID,
				ControlRevision: shared.revision,
			},
			Messages: pending.messages, Events: pending.events,
		},
	}
}

func (t *runtimeTurn) retainRootPhaseProjection(
	projection session.ExecutionRootProjection,
	events []Event,
) {
	t.checkpointMutex.Lock()
	defer t.checkpointMutex.Unlock()

	t.mutex.Lock()
	defer t.mutex.Unlock()

	// Insert before pending writes so their checkpoint marks stay accurate.
	t.messages = slices.Insert(t.messages, t.checkpointedMessages,
		projection.Messages...)
	t.events = slices.Insert(t.events, t.checkpointedEvents, events...)
	t.checkpointedMessages += len(projection.Messages)
	t.checkpointedEvents += len(events)
}

func rootPhaseTranscript(result any) *session.ExecutionRootProjection {
	if opening, ok := result.(workflowOpeningResult); ok {
		if opening.RootProjection != nil {
			return &opening.RootProjection.Transcript
		}

		return nil
	}

	if model, ok := result.(workflowRoundResult); ok {
		if model.RootProjection != nil {
			return &model.RootProjection.Transcript
		}

		return nil
	}

	phase, ok := result.(workflowToolPhaseState)
	if !ok || phase.RootProjection == nil {
		return nil
	}

	return &phase.RootProjection.Transcript
}
