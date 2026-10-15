package agent

import (
	"context"
	"errors"

	"github.com/cschleiden/go-workflows/backend"
	"github.com/cschleiden/go-workflows/backend/history"
	"github.com/cschleiden/go-workflows/client"
	"github.com/cschleiden/go-workflows/workflow"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
)

// The SDK otherwise generates a new execution ID on every create attempt.
type workflowSubmissionBackend struct {
	backend.Backend
}

func newWorkflowClient(storage backend.Backend) *client.Client {
	return client.New(&workflowSubmissionBackend{Backend: storage})
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
