package session

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

func (s *Store) claimExecutionChild(
	ctx context.Context,
	tx *repositories.Query,
	execution *models.Execution,
	generationID uuid.UUID,
) error {
	if execution.Kind != models.ExecutionKindChild {
		return nil
	}

	if execution.AgentRunID == nil {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"child execution has no agent run")
	}

	run, err := s.findAgentRunWithQuery(ctx, tx, execution.SessionID,
		*execution.AgentRunID)
	if err != nil {
		return ctxerrors.Wrap(err, "read activity child owner")
	}

	if run.CancelRequested || run.ParentTurnID != execution.RootTurnID {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"activity child is stopped or belongs to another turn")
	}

	if run.State == models.AgentRunStateRunning &&
		run.WorkerGenerationID == generationID.String() {
		return nil
	}

	if !recoverableAgentRun(run, generationID) {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"activity child cannot be recovered")
	}

	if err := claimAgentRunWithQuery(ctx, tx, run, generationID); err != nil {
		return ctxerrors.Wrap(err, "transfer activity child ownership")
	}

	return nil
}
