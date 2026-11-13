package db

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/go-workflows/backend"
	"github.com/psyb0t/go-workflows/backend/history"
	"github.com/psyb0t/go-workflows/core"
	"github.com/psyb0t/go-workflows/diag"
	"github.com/psyb0t/go-workflows/workflow"
)

// Task claims and mutations stay on Backend's single writer. Inspection uses
// a separate query-only backend so it cannot queue behind those transactions.
type workflowReadBackend struct {
	diag.Backend
	reads diag.Backend
}

func (b *workflowReadBackend) GetWorkflowInstanceState(
	ctx context.Context, instance *workflow.Instance,
) (core.WorkflowInstanceState, error) {
	state, err := b.reads.GetWorkflowInstanceState(ctx, instance)
	if err != nil {
		return state, ctxerrors.Wrap(err, "read workflow state")
	}

	return state, nil
}

func (b *workflowReadBackend) GetWorkflowInstanceHistory(
	ctx context.Context, instance *workflow.Instance, after *int64,
) ([]*history.Event, error) {
	events, err := b.reads.GetWorkflowInstanceHistory(ctx, instance, after)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read workflow history")
	}

	return events, nil
}

func (b *workflowReadBackend) GetStats(
	ctx context.Context,
) (*backend.Stats, error) {
	stats, err := b.reads.GetStats(ctx)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read workflow stats")
	}

	return stats, nil
}

func (b *workflowReadBackend) GetWorkflowInstance(
	ctx context.Context, instance *workflow.Instance,
) (*diag.WorkflowInstanceRef, error) {
	ref, err := b.reads.GetWorkflowInstance(ctx, instance)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read workflow instance")
	}

	return ref, nil
}

func (b *workflowReadBackend) GetWorkflowInstances(
	ctx context.Context, afterID, afterExecutionID string, count int,
) ([]*diag.WorkflowInstanceRef, error) {
	refs, err := b.reads.GetWorkflowInstances(ctx, afterID, afterExecutionID,
		count)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read workflow instances")
	}

	return refs, nil
}

func (b *workflowReadBackend) GetWorkflowTree(
	ctx context.Context, instance *workflow.Instance,
) (*diag.WorkflowInstanceTree, error) {
	tree, err := b.reads.GetWorkflowTree(ctx, instance)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read workflow tree")
	}

	return tree, nil
}
