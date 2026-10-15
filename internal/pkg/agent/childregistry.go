package agent

import (
	"context"
	"math"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

func (r *AgentRunRegistry) restoreWorkflowRun(
	stored *models.AgentRun,
	cancel context.CancelFunc,
	events []AgentRunEvent,
) (*AgentRun, error) {
	if err := validateWorkflowRegistryRun(r.sessionID, stored,
		cancel); err != nil {
		return nil, ctxerrors.Wrap(err, "validate saved registry child")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if existing := r.runs[stored.ID]; existing != nil {
		if !sameWorkflowRegistryRun(existing.Snapshot(), stored) {
			return nil, ctxerrors.Wrap(commerr.ErrConflict,
				"saved child differs from its registry entry")
		}

		return existing, nil
	}

	run := &AgentRun{
		ID: stored.ID, SessionID: stored.SessionID,
		ParentTurnID:     stored.ParentTurnID,
		ParentAgentRunID: cloneAgentRunID(stored.ParentAgentRunID),
		ParentToolCallID: stored.ParentToolCallID,
		RequestID:        stored.RequestID, Name: stored.Name,
		Definition: string(stored.Definition), Depth: int(stored.Depth),
		StartedAt: stored.StartedAt, state: string(stored.State),
		cancel: cancel, done: make(chan struct{}),
		events: newAgentRunEventBuffer(r.limits.MaxEventCount,
			r.limits.MaxEventBytes),
	}
	if stored.EndedAt != nil {
		run.endedAt = *stored.EndedAt
	}

	for _, event := range events {
		run.AppendStoredEvent(event)
	}

	if stored.State != models.AgentRunStateRunning {
		close(run.done)
	}

	r.runs[run.ID] = run

	return run, nil
}

func validateWorkflowRegistryRun(
	sessionID uuid.UUID,
	stored *models.AgentRun,
	cancel context.CancelFunc,
) error {
	if stored == nil || cancel == nil {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"saved registry child and cancellation")
	}

	if stored.SessionID != sessionID {
		return ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"registry child belongs to another session")
	}

	if invalidWorkflowRegistryIdentity(stored) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"saved registry child identity")
	}

	switch stored.State {
	case models.AgentRunStateRunning, models.AgentRunStateCompleted,
		models.AgentRunStateFailed, models.AgentRunStateCancelled,
		models.AgentRunStateInterrupted:
		return nil
	}

	return ctxerrors.Wrap(commerr.ErrValidationFailed,
		"saved registry child state")
}

func invalidWorkflowRegistryIdentity(stored *models.AgentRun) bool {
	return stored.ID == uuid.Nil || stored.ParentTurnID == uuid.Nil ||
		stored.RequestID == uuid.Nil || stored.Name == "" ||
		stored.Depth < 1 || stored.Depth > int64(math.MaxInt) ||
		stored.StartedAt.IsZero()
}

func sameWorkflowRegistryRun(
	current AgentRunSnapshot,
	stored *models.AgentRun,
) bool {
	return current.SessionID == stored.SessionID &&
		current.ParentTurnID == stored.ParentTurnID &&
		sameChildBootstrapParent(current.ParentAgentRunID,
			stored.ParentAgentRunID) &&
		current.ParentToolCallID == stored.ParentToolCallID &&
		current.RequestID == stored.RequestID && current.Name == stored.Name &&
		current.Definition == string(stored.Definition) &&
		int64(current.Depth) == stored.Depth &&
		current.StartedAt.Equal(stored.StartedAt) &&
		current.State == string(stored.State)
}
