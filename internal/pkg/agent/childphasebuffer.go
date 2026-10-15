package agent

import (
	"context"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/session"
)

type childPhaseBuffer struct {
	ActivityID  uuid.UUID                        `json:"activityId"`
	EventCursor int64                            `json:"eventCursor"`
	Transcript  session.ExecutionChildProjection `json:"transcript"`
}

func newChildPhaseSink(
	ctx context.Context,
	original *agentRunSink,
	activityID uuid.UUID,
	revision int64,
) (*agentRunSink, error) {
	run, err := original.store.GetAgentRun(ctx, original.sessionID,
		original.run.ID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read child event cursor")
	}

	sink := newAgentRunSink(original.store, original.sessionID, original.run,
		nil, original.modelReference)
	sink.phaseBuffer = &childPhaseBuffer{
		ActivityID:  activityID,
		EventCursor: run.EventCount,
		Transcript: session.ExecutionChildProjection{
			ControlRevision: revision,
		},
	}

	return sink, nil
}

func (s *agentRunSink) bufferPhaseEvent(kind, payload string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	buffer := s.phaseBuffer
	index := len(buffer.Transcript.Events)
	buffer.Transcript.Events = append(buffer.Transcript.Events,
		session.AgentRunEventInput{
			ID: uuid.NewSHA1(buffer.ActivityID,
				[]byte(strconv.Itoa(index))),
			EventType: kind, PayloadJSON: payload, CreatedAt: time.Now().UTC(),
		})
}

func childPhaseTranscript(result any) *session.ExecutionChildProjection {
	if model, ok := result.(workflowRoundResult); ok {
		if model.ChildProjection != nil {
			return &model.ChildProjection.Transcript
		}

		return nil
	}

	phase, ok := result.(workflowToolPhaseState)
	if !ok || phase.ChildProjection == nil {
		return nil
	}

	return &phase.ChildProjection.Transcript
}
