package session

import (
	"context"
	"encoding/json"
	"math"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

type executionChildTerminalPayload struct {
	AgentRunID    uuid.UUID            `json:"agentRunId"`
	State         models.AgentRunState `json:"state"`
	FailureClass  string               `json:"failureClassification,omitempty"`
	FailureDetail string               `json:"failureDetail,omitempty"`
}

func (s *Store) recordExecutionChildTerminal(
	ctx context.Context,
	tx *repositories.Query,
	run *models.AgentRun,
) (*models.AgentRunEvent, error) {
	payload, err := json.Marshal(executionChildTerminalPayload{
		AgentRunID: run.ID, State: run.State,
		FailureClass:  run.FailureClassification,
		FailureDetail: run.FailureDetail,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode child terminal event")
	}

	kind := EventTypeAgentRunCompleted
	if run.State == models.AgentRunStateFailed {
		kind = EventTypeAgentRunFailed
	}

	if run.State == models.AgentRunStateCancelled {
		kind = EventTypeAgentRunCancelled
	}

	event, err := s.appendExecutionChildEvent(ctx, tx, run, kind, payload)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "save child terminal event")
	}

	return event, nil
}

// appendExecutionChildEvent stores one agent.run.* event for run in tx and
// gives it the session's next feed sequence. tx must be a feed transaction.
func (s *Store) appendExecutionChildEvent(
	ctx context.Context,
	tx *repositories.Query,
	run *models.AgentRun,
	kind string,
	payload []byte,
) (*models.AgentRunEvent, error) {
	if run.EventCount == math.MaxInt64 {
		return nil, ctxerrors.Wrap(commerr.ErrInvalidState,
			"child event counter is exhausted")
	}

	sequence, err := nextAgentRunEventSequence(ctx, tx, run.ID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "allocate child event sequence")
	}

	feedSequence, err := s.allocateFeedSequences(ctx, tx, run.SessionID, 1)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "allocate child event feed sequence")
	}

	event := &models.AgentRunEvent{
		ID: s.newID(), SessionID: run.SessionID, AgentRunID: run.ID,
		Sequence: sequence, FeedSequence: feedSequence, EventType: kind,
		PayloadJSON: string(payload), CreatedAt: s.now(),
	}
	if err := tx.AgentRunEvent.WithContext(ctx).Create(event); err != nil {
		return nil, ctxerrors.Wrap(err, "save child event")
	}

	repository := tx.AgentRun

	_, err = repository.WithContext(ctx).Where(
		repository.ID.Eq(run.ID), repository.SessionID.Eq(run.SessionID),
	).UpdateSimple(repository.EventCount.Add(1))
	if err != nil {
		return nil, ctxerrors.Wrap(err, "count child event")
	}

	return event, nil
}
