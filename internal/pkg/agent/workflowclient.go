package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/go-workflows/backend"
	"github.com/psyb0t/go-workflows/backend/history"
	"github.com/psyb0t/go-workflows/client"
	"github.com/psyb0t/go-workflows/core"
	"github.com/psyb0t/go-workflows/workflow"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// The SDK otherwise generates a new execution ID on every create attempt.
type workflowSubmissionBackend struct {
	backend.Backend
	store session.ExecutionStorage
}

func newWorkflowClient(storage backend.Backend,
	stores ...session.ExecutionStorage,
) *client.Client {
	wrapped := &workflowSubmissionBackend{Backend: storage}
	if len(stores) > 0 {
		wrapped.store = stores[0]
	}

	return client.New(wrapped)
}

func (b *workflowSubmissionBackend) resolve(ctx context.Context,
	instance *workflow.Instance,
) (*workflow.Instance, error) {
	start := *instance
	if b.store != nil {
		cached, err := b.cachedWorkflowExecutionID(ctx, instance.InstanceID)
		if err != nil {
			return nil, err
		}

		if cached != "" {
			start.ExecutionID = cached
		}
	}

	current, err := db.ResolveWorkflowExecution(ctx, b.Backend, &start)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resolve native workflow execution")
	}

	return current, nil
}

func (b *workflowSubmissionBackend) cachedWorkflowExecutionID(
	ctx context.Context, instanceID string,
) (string, error) {
	sessionPart, executionPart, ok := strings.Cut(instanceID, "/")
	if !ok {
		return "", nil
	}

	sessionID, err := uuid.Parse(sessionPart)
	if err != nil {
		return "", ctxerrors.Wrap(err, "parse workflow session identity")
	}

	executionID, err := uuid.Parse(executionPart)
	if err != nil {
		return "", ctxerrors.Wrap(err, "parse workflow execution identity")
	}

	saved, err := b.store.GetExecution(ctx, sessionID, executionID)
	if err != nil {
		return "", ctxerrors.Wrap(err, "read cached workflow pointer")
	}

	return saved.WorkflowExecutionID, nil
}

func (b *workflowSubmissionBackend) GetWorkflowInstanceState(
	ctx context.Context, instance *workflow.Instance,
) (core.WorkflowInstanceState, error) {
	state, err := b.Backend.GetWorkflowInstanceState(ctx, instance)
	if err != nil {
		return state, ctxerrors.Wrap(err, "read native workflow state")
	}

	if state != core.WorkflowInstanceStateContinuedAsNew {
		return state, nil
	}

	for range db.WorkflowContinuationLimit {
		current, err := b.resolve(ctx, instance)
		if err != nil {
			return state, ctxerrors.Wrap(err, "resolve workflow state")
		}

		state, err = b.Backend.GetWorkflowInstanceState(ctx, current)
		if err != nil {
			return state, ctxerrors.Wrap(err, "read resolved workflow state")
		}

		if state != core.WorkflowInstanceStateContinuedAsNew {
			return state, nil
		}

		instance = current
	}

	return state, ctxerrors.Wrap(commerr.ErrInvalidState,
		"workflow state exceeded continuation limit")
}

func (b *workflowSubmissionBackend) GetWorkflowInstanceHistory(
	ctx context.Context, instance *workflow.Instance, after *int64,
) ([]*history.Event, error) {
	current, err := b.resolve(ctx, instance)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resolve workflow history")
	}

	events, err := b.Backend.GetWorkflowInstanceHistory(ctx, current, after)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read resolved workflow history")
	}

	return events, nil
}

func (b *workflowSubmissionBackend) CancelWorkflowInstance(
	ctx context.Context, instance *workflow.Instance, event *history.Event,
) error {
	for range db.WorkflowContinuationLimit {
		current, err := b.resolve(ctx, instance)
		if err != nil {
			return ctxerrors.Wrap(err, "resolve workflow cancellation")
		}

		cancelErr := b.Backend.CancelWorkflowInstance(ctx, current, event)

		next, err := b.resolve(ctx, current)
		if err != nil {
			return ctxerrors.Wrap(err, "recheck workflow cancellation")
		}

		if current.ExecutionID == next.ExecutionID {
			if cancelErr != nil {
				return ctxerrors.Wrap(cancelErr, "cancel current workflow")
			}

			return nil
		}

		instance = next
	}

	return ctxerrors.Wrap(commerr.ErrInvalidState,
		"workflow cancellation exceeded continuation limit")
}

func (b *workflowSubmissionBackend) CreateWorkflowInstance(
	ctx context.Context,
	instance *workflow.Instance,
	event *history.Event,
) error {
	if instance == nil || instance.InstanceID == "" || event == nil {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"workflow submission identity")
	}

	if err := ctx.Err(); err != nil {
		return ctxerrors.Wrap(err, "admit workflow submission")
	}

	instance.ExecutionID = uuid.NewSHA1(uuid.NameSpaceOID,
		[]byte(instance.InstanceID)).String()

	_, err := b.GetWorkflowInstanceState(ctx, instance)
	if err == nil {
		ctxscope.GetLogger(ctx).Debug("workflow submission reused",
			"instance_id", instance.InstanceID,
			"execution_id", instance.ExecutionID)

		return nil
	}

	if !errors.Is(err, backend.ErrInstanceNotFound) {
		return ctxerrors.Wrap(err, "read submitted workflow identity")
	}

	err = b.Backend.CreateWorkflowInstance(ctx, instance, event)
	if err != nil {
		return b.reconcileSubmission(ctx, instance, err)
	}

	return nil
}

func (b *workflowSubmissionBackend) reconcileSubmission(
	ctx context.Context,
	instance *workflow.Instance,
	cause error,
) error {
	if _, err := b.GetWorkflowInstanceState(ctx, instance); err != nil {
		return ctxerrors.Wrap(errors.Join(cause, err),
			"reconcile workflow submission")
	}

	ctxscope.GetLogger(ctx).Info("workflow submission reconciled",
		"instance_id", instance.InstanceID,
		"execution_id", instance.ExecutionID)

	return nil
}
