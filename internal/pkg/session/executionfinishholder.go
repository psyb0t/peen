package session

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

func validateExecutionFinishHolder(holder *ExecutionFinishHolder) error {
	if holder == nil {
		return nil
	}

	if holder.LeaseID == uuid.Nil || holder.ControlRevision < 0 {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"final root holder identity")
	}

	return nil
}

func checkExecutionFinishHolder(
	ctx context.Context,
	query *repositories.Query,
	activity *models.ExecutionActivity,
	execution *models.Execution,
	input *FinishExecutionInput,
) error {
	if input == nil || input.RootHolder == nil {
		return nil
	}

	if execution.Kind != models.ExecutionKindRoot {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"final holder belongs to a root execution")
	}

	holder, err := findExecutionTurnLease(ctx, query, execution)
	if err != nil {
		return ctxerrors.Wrap(err, "read final root holder")
	}

	proof := input.RootHolder
	if holder.LeaseID != proof.LeaseID ||
		holder.OwnerGenerationID.String() != activity.OwnerGenerationID ||
		holder.ControlRevision != proof.ControlRevision ||
		execution.ControlRevision != proof.ControlRevision {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"final root callback holder changed")
	}

	return nil
}
