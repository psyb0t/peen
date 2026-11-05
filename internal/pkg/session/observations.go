package session

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/metrics"
)

type observationBufferKey struct{}

// observationBuffer holds what a transaction proved until it commits, so a
// rolled-back write is never counted.
type observationBuffer struct {
	mutex   sync.Mutex
	pending []metrics.Observation
}

// withObservations returns a context that buffers observations, and the
// function that records them. Call it only after the transaction committed.
func (s *Store) withObservations(
	ctx context.Context,
) (context.Context, func()) {
	buffer := &observationBuffer{}

	return context.WithValue(ctx, observationBufferKey{}, buffer), func() {
		buffer.mutex.Lock()
		defer buffer.mutex.Unlock()

		for _, observation := range buffer.pending {
			s.metrics.Record(observation)
		}

		buffer.pending = nil
	}
}

// observe records now, or at commit when ctx belongs to a transaction.
func (s *Store) observe(ctx context.Context, observation metrics.Observation) {
	buffer, buffered := ctx.Value(observationBufferKey{}).(*observationBuffer)
	if !buffered {
		s.metrics.Record(observation)

		return
	}

	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()

	buffer.pending = append(buffer.pending, observation)
}

func (s *Store) observeTurnStarted(ctx context.Context, turn *models.Turn) {
	s.observe(ctx, metrics.Observation{
		Kind:               metrics.KindTurnStarted,
		SessionID:          turn.SessionID.String(),
		WorkerGenerationID: turn.WorkerGenerationID,
		TurnID:             turn.ID.String(),
	})
}

func (s *Store) observeTurnFinished(
	ctx context.Context,
	tx *repositories.Query,
	lease Lease,
	state models.TurnState,
) error {
	if s.metrics == nil {
		return nil
	}

	turn := tx.Turn

	finished, err := turn.WithContext(ctx).Where(
		turn.ID.Eq(lease.TurnID),
		turn.SessionID.Eq(lease.SessionID),
	).First()
	if err != nil {
		return ctxerrors.Wrap(err, "read finished turn")
	}

	s.observe(ctx, metrics.Observation{
		Kind:               metrics.KindTurn,
		SessionID:          lease.SessionID.String(),
		WorkerGenerationID: finished.WorkerGenerationID,
		TurnID:             lease.TurnID.String(),
		Outcome:            turnOutcome(state),
		DurationMS:         s.now().Sub(finished.StartedAt).Milliseconds(),
	})

	return nil
}

func (s *Store) observeAgentRunStarted(
	ctx context.Context,
	run *models.AgentRun,
) {
	s.observe(ctx, metrics.Observation{
		Kind:       metrics.KindAgentRunStarted,
		SessionID:  run.SessionID.String(),
		TurnID:     run.ParentTurnID.String(),
		AgentRunID: run.ID.String(),
		Name:       run.Name,
	})
}

func (s *Store) observeAgentRunFinished(
	ctx context.Context,
	run *models.AgentRun,
) {
	s.observe(ctx, metrics.Observation{
		Kind:         metrics.KindAgentRun,
		SessionID:    run.SessionID.String(),
		TurnID:       run.ParentTurnID.String(),
		AgentRunID:   run.ID.String(),
		Name:         run.Name,
		Outcome:      agentRunOutcome(run.State),
		DurationMS:   s.now().Sub(run.StartedAt).Milliseconds(),
		InputTokens:  run.PromptTokenCount,
		OutputTokens: run.CompletionTokenCount,
	})
}

func (s *Store) observeCompaction(
	ctx context.Context,
	sessionID uuid.UUID,
	agentRunID *uuid.UUID,
	inputTokens int64,
	summaryTokens int64,
	sourceMessages int64,
) {
	observation := metrics.Observation{
		Kind:         metrics.KindCompaction,
		SessionID:    sessionID.String(),
		InputTokens:  inputTokens,
		OutputTokens: summaryTokens,
		Count:        sourceMessages,
	}
	if agentRunID != nil {
		observation.AgentRunID = agentRunID.String()
	}

	s.observe(ctx, observation)
}

func turnOutcome(state models.TurnState) string {
	//nolint:exhaustive // Running never reaches a terminal observation.
	switch state {
	case models.TurnStateCompleted:
		return metrics.OutcomeSuccess
	case models.TurnStateCancelled:
		return metrics.OutcomeCancelled
	default:
		return metrics.OutcomeError
	}
}

func agentRunOutcome(state models.AgentRunState) string {
	//nolint:exhaustive // Running never reaches a terminal observation.
	switch state {
	case models.AgentRunStateCompleted:
		return metrics.OutcomeSuccess
	case models.AgentRunStateCancelled:
		return metrics.OutcomeCancelled
	default:
		return metrics.OutcomeError
	}
}
