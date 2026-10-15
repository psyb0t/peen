package agent

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// PauseSessionWorkflows retains unfinished work and cancels its activities.
// It releases the current root turn so another request can start new work.
func (r *Runtime) PauseSessionWorkflows(ctx context.Context) (bool, error) {
	native := r.nativeWorkflowRunner()
	if native == nil {
		return false, nil
	}

	items, err := native.sessionExecutions(ctx, r.sessionID)
	if err != nil {
		return false, ctxerrors.Wrap(err, "select session workflows to pause")
	}

	var stopErrors []error

	for _, item := range items {
		if err := native.pauseExecution(ctx, item); err != nil {
			ctxscope.GetLogger(ctx).Warn("session workflow pause failed",
				"execution_id", item.ID, "reason", "pause_failed", "err", err)
			stopErrors = append(stopErrors, err)
		}
	}

	ctxscope.GetLogger(ctx).Info("session workflows pause requested",
		"execution_count", len(items), "failed_count", len(stopErrors))

	return len(items) > 0, errors.Join(stopErrors...)
}

func (n *nativeWorkflowRunner) sessionExecutions(
	ctx context.Context,
	sessionID uuid.UUID,
) ([]*models.Execution, error) {
	var items []*models.Execution

	options := session.SearchExecutionsOptions{}
	for {
		page, err := n.engine.options.Store.SearchExecutions(ctx, sessionID,
			options)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "read session workflow page")
		}

		for _, item := range page.Items {
			if item.State == models.ExecutionStateCompleted ||
				item.State == models.ExecutionStateFailed ||
				item.State == models.ExecutionStateCancelled {
				continue
			}

			items = append(items, item)
		}

		if !page.HasMore {
			return items, nil
		}

		options.Offset += len(page.Items)
	}
}

func (n *nativeWorkflowRunner) pauseExecution(
	ctx context.Context,
	execution *models.Execution,
) error {
	if err := n.engine.pause(ctx, execution.SessionID, execution.ID,
		workflowUserStopReason); err != nil {
		return ctxerrors.Wrap(err, "pause selected session workflow")
	}

	// A controller may already have paused this row before the worker command.
	n.engine.cancelActivities([]uuid.UUID{execution.ID})

	if execution.Kind != models.ExecutionKindRoot {
		return nil
	}

	if err := n.dispatch.root.retirePaused(ctx, n.engine.options.Store,
		execution); err != nil {
		return ctxerrors.Wrap(err, "release paused session root")
	}

	return nil
}
