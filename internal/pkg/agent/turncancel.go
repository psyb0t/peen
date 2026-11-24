package agent

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// InterruptTurn reaches only this turn's local children and workflows after
// its cancellation was recorded by the controller.
func (r *Runtime) InterruptTurn(ctx context.Context, turnID uuid.UUID) error {
	if turnID == uuid.Nil {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"turn interruption requires a turn ID")
	}

	r.cancelTurnAgentRuns(ctx, r.sessionID, turnID)

	native := r.nativeWorkflowRunner()
	if native == nil {
		return nil
	}

	executions, err := native.sessionExecutions(ctx, r.sessionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read interrupted turn executions")
	}

	var failures []error

	for _, execution := range executions {
		if execution.RootTurnID != turnID {
			continue
		}

		if err := native.pauseExecution(ctx, execution); err != nil {
			failures = append(failures,
				ctxerrors.Wrap(err, "pause interrupted turn execution"))
		}
	}

	return errors.Join(failures...)
}
