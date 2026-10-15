package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/cschleiden/go-workflows/backend"
	"github.com/cschleiden/go-workflows/core"
	"github.com/cschleiden/go-workflows/workflow"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const workflowCapacityPauseReason = "active child agent limit reached"

func rootAdmissionRefused(execution *models.Execution, cause error) bool {
	if execution.Kind != models.ExecutionKindRoot ||
		!errors.Is(cause, errRootAdmissionBusy) {
		return false
	}

	return execution.State == models.ExecutionStateQueued ||
		execution.State == models.ExecutionStateRunning ||
		execution.State == models.ExecutionStateReconciling ||
		execution.State == models.ExecutionStatePaused
}

func (e *workflowExecution) pauseForCapacity(
	ctx context.Context,
	execution workflowExecutionInput,
	cause error,
) error {
	if !errors.Is(cause, ErrTooManyAgentRuns) {
		return cause
	}

	if err := e.pause(ctx, execution.SessionID, execution.ExecutionID,
		workflowCapacityPauseReason); err != nil {
		err = ctxerrors.Wrap(err, "pause resource limited workflow")
		ctxscope.GetLogger(ctx).Warn("capacity pause cleanup incomplete",
			"execution_id", execution.ExecutionID, "err", err)

		return errors.Join(cause, err)
	}

	return cause
}

func (e *workflowExecution) activityContext(
	ctx context.Context,
	input workflowExecutionInput,
) (context.Context, func()) {
	ctx = runtimecontext.WithLoggerFrom(ctx, e.options.Lifecycle)
	ctx = ctxscope.Set(ctx,
		ctxscope.Attr("session_id", input.SessionID.String()),
		ctxscope.Attr("execution_id", input.ExecutionID.String()),
		ctxscope.Attr("worker_generation_id", e.options.GenerationID.String()),
	)
	ctx, cancel := context.WithCancel(ctx)
	stopLifecycle := context.AfterFunc(e.options.Lifecycle, cancel)
	id := uuid.New()

	e.mutex.Lock()
	if e.active == nil {
		e.active = make(map[uuid.UUID]map[uuid.UUID]context.CancelFunc)
	}

	if e.active[input.ExecutionID] == nil {
		e.active[input.ExecutionID] = make(map[uuid.UUID]context.CancelFunc)
	}

	e.active[input.ExecutionID][id] = cancel
	e.mutex.Unlock()

	return ctx, func() {
		stopLifecycle()
		cancel()
		e.mutex.Lock()
		defer e.mutex.Unlock()

		delete(e.active[input.ExecutionID], id)

		if len(e.active[input.ExecutionID]) == 0 {
			delete(e.active, input.ExecutionID)
		}
	}
}

func (e *workflowExecution) pause(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
	reason string,
) error {
	ids, err := e.options.Store.PauseExecution(
		ctx, sessionID, executionID, reason,
	)
	if err != nil {
		return ctxerrors.Wrap(err, "pause workflow execution tree")
	}

	// A controller may have saved the target pause before this interrupt.
	ids = append(ids, executionID)
	e.cancelActivities(ids)

	if e.options.AfterPause != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx),
			workflowPauseTimeout)
		defer cancel()

		if err := e.options.AfterPause(cleanup, sessionID, ids); err != nil {
			return ctxerrors.Wrap(err, "release paused workflow callbacks")
		}
	}

	return nil
}

func (e *workflowExecution) cancelActivities(ids []uuid.UUID) {
	e.mutex.Lock()
	defer e.mutex.Unlock()

	for _, id := range ids {
		for _, cancel := range e.active[id] {
			cancel()
		}
	}
}

func (n *nativeWorkflowRunner) cancelChildTree(
	ctx context.Context,
	run *models.AgentRun,
) error {
	targetID := uuid.NewSHA1(run.ID, []byte(childWorkflowIdentity))

	target, err := n.engine.options.Store.GetExecution(ctx,
		run.SessionID, targetID)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return nil
		}

		return ctxerrors.Wrap(err, "read cancelled native child")
	}

	pending := []*models.Execution{target}
	for len(pending) != 0 {
		execution := pending[0]
		pending = pending[1:]

		if execution.State == models.ExecutionStateCancelled {
			if err := n.cancelChildExecution(ctx, execution); err != nil {
				return ctxerrors.Wrap(err, "interrupt cancelled native child")
			}
		}

		options := session.SearchExecutionsOptions{ParentID: execution.ID}
		for {
			page, err := n.engine.options.Store.SearchExecutions(ctx,
				run.SessionID, options)
			if err != nil {
				return ctxerrors.Wrap(err, "read cancelled native descendants")
			}

			pending = append(pending, page.Items...)
			if !page.HasMore {
				break
			}

			options.Offset += len(page.Items)
		}
	}

	return nil
}

func (n *nativeWorkflowRunner) cancelChildExecution(
	ctx context.Context,
	execution *models.Execution,
) error {
	if execution.Kind != models.ExecutionKindChild ||
		execution.AgentRunID == nil {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"cancelled execution has no child owner")
	}

	n.engine.cancelActivities([]uuid.UUID{execution.ID})
	n.dispatch.runtime.CancelLocalAgentRun(execution.SessionID,
		*execution.AgentRunID)

	if err := n.cancelChildSDK(ctx, execution); err != nil {
		return ctxerrors.Wrap(err, "cancel native child SDK instance")
	}

	callbacks := n.dispatch.child
	slot := callbacks.slot(execution.ID)

	release, err := slot.gate.acquire(ctx, true)
	if err != nil {
		return ctxerrors.Wrap(err, "admit cancelled child cleanup")
	}

	if slot.current != nil {
		slot.current.cancel()
	}

	err = callbacks.retireChildCallback(ctx, slot, execution)

	release()

	if err != nil {
		return ctxerrors.Wrap(err, "retire cancelled child callbacks")
	}

	return n.dispatch.notifyChildReport(ctx, workflowExecutionInput{
		SessionID: execution.SessionID, ExecutionID: execution.ID,
	})
}

func (n *nativeWorkflowRunner) reconcileCancelledChildren(
	ctx context.Context,
) error {
	options := session.SearchExecutionsOptions{
		State: models.ExecutionStateCancelled,
	}
	for {
		page, err := n.engine.options.Store.SearchExecutions(ctx,
			n.dispatch.runtime.sessionID, options)
		if err != nil {
			return ctxerrors.Wrap(err, "read retained child cancellations")
		}

		for _, execution := range page.Items {
			if err := n.cancelChildExecution(ctx, execution); err != nil {
				return ctxerrors.Wrap(err, "restore child cancellation")
			}
		}

		if !page.HasMore {
			return nil
		}

		options.Offset += len(page.Items)
	}
}

func (n *nativeWorkflowRunner) cancelChildSDK(
	ctx context.Context,
	execution *models.Execution,
) error {
	instanceID := fmt.Sprintf("%s/%s", execution.SessionID, execution.ID)
	instance := &workflow.Instance{
		InstanceID: instanceID,
		ExecutionID: uuid.NewSHA1(uuid.NameSpaceOID,
			[]byte(instanceID)).String(),
	}

	state, err := n.api.GetWorkflowInstanceState(ctx, instance)
	if err != nil && !errors.Is(err, backend.ErrInstanceNotFound) {
		return ctxerrors.Wrap(err, "read cancelled SDK workflow")
	}

	if err == nil && state != core.WorkflowInstanceStateFinished {
		if err := n.api.CancelWorkflowInstance(ctx, instance); err != nil {
			return ctxerrors.Wrap(err, "cancel SDK child workflow")
		}
	}

	return nil
}
