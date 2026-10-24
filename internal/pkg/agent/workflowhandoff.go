package agent

import (
	"context"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/go-workflows/backend"
	"github.com/psyb0t/go-workflows/core"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

func (n *nativeWorkflowRunner) restoreWorkflowControls(
	ctx context.Context,
) error {
	if err := n.reconcileCancelledChildren(ctx); err != nil {
		return ctxerrors.Wrap(err, "restore native child cancellations")
	}

	return n.nudgeAdmittedExecutions(ctx)
}

func (n *nativeWorkflowRunner) nudgeAdmittedExecutions(
	ctx context.Context,
) error {
	items, err := n.sessionExecutions(ctx, n.dispatch.runtime.sessionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read admitted workflow handoff")
	}

	hasAdmittedRoot := false

	for _, execution := range items {
		if execution.State == models.ExecutionStatePaused {
			continue
		}

		if execution.Kind == models.ExecutionKindRoot {
			hasAdmittedRoot = true
		}

		if err := n.nudgeExecution(ctx, execution); err != nil {
			return ctxerrors.Wrap(err, "signal admitted workflow handoff")
		}
	}

	if hasAdmittedRoot {
		return nil
	}

	if err := n.scheduleDeferredRoot(ctx); err != nil {
		return ctxerrors.Wrap(err, "handoff retained deferred input")
	}

	return n.dispatch.runtime.wakeRetainedQuestions(ctx)
}

func (n *nativeWorkflowRunner) nudgeExecution(
	ctx context.Context,
	execution *models.Execution,
) error {
	instance := nativeWorkflowInstance(execution.SessionID, execution.ID)
	instanceID := instance.InstanceID

	state, err := n.api.GetWorkflowInstanceState(ctx, instance)
	if errors.Is(err, backend.ErrInstanceNotFound) {
		if execution.WorkflowExecutionID == "" {
			return nil
		}

		waiting, err := n.waitingForRootBinding(ctx, execution)
		if err != nil {
			return ctxerrors.Wrap(err, "check root submission handoff")
		}

		if waiting {
			ctxscope.GetLogger(ctx).Debug("root submission binding pending",
				"execution_id", execution.ID, "reason", "caller_is_binding")

			return nil
		}

		if err := n.submitRetainedExecution(ctx, execution); err != nil {
			return ctxerrors.Wrap(err, "recover missing SDK submission")
		}

		return nil
	}

	if err != nil {
		return ctxerrors.Wrap(err, "read admitted SDK workflow")
	}

	if state == core.WorkflowInstanceStateFinished {
		return nil
	}

	if err := n.api.SignalWorkflow(ctx, instanceID, workflowResumeSignal,
		struct{}{}); err != nil {
		return ctxerrors.Wrap(err, "signal admitted SDK workflow")
	}

	ctxscope.GetLogger(ctx).Info("admitted workflow signaled",
		"execution_id", execution.ID, "session_id", execution.SessionID)

	return nil
}

func (n *nativeWorkflowRunner) waitingForRootBinding(
	ctx context.Context,
	execution *models.Execution,
) (bool, error) {
	if execution.Kind != models.ExecutionKindRoot ||
		!n.dispatch.runtime.store.IsActive(execution.SessionID) {
		return false, nil
	}

	callbacks := n.dispatch.root
	callbacks.mutex.Lock()
	slot := callbacks.slots[execution.ID]
	callbacks.mutex.Unlock()

	if slot == nil {
		return true, nil
	}

	release, err := slot.gate.acquire(ctx, false)
	if err != nil {
		return false, ctxerrors.Wrap(err, "inspect pending root binding")
	}
	defer release()

	return slot.current == nil, nil
}
