package db

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/go-workflows/backend"
	"github.com/psyb0t/go-workflows/backend/history"
	"github.com/psyb0t/go-workflows/core"
	"github.com/psyb0t/go-workflows/workflow"
)

// ResolveWorkflowExecution follows committed handoffs. A missing successor
// is corruption, never permission to submit the initial workflow again.
func ResolveWorkflowExecution(
	ctx context.Context,
	storage backend.Backend,
	instance *workflow.Instance,
) (*workflow.Instance, error) {
	current := *instance
	for depth := 0; depth <= WorkflowContinuationLimit; depth++ {
		state, err := storage.GetWorkflowInstanceState(ctx, &current)
		if err != nil {
			if depth > 0 && errors.Is(err, backend.ErrInstanceNotFound) {
				return nil, ctxerrors.Wrap(commerr.ErrInvalidState,
					"workflow continuation successor unavailable")
			}

			return nil, ctxerrors.Wrap(err, "read current workflow execution")
		}

		if state != core.WorkflowInstanceStateContinuedAsNew {
			return &current, nil
		}

		if depth == WorkflowContinuationLimit {
			break
		}

		events, err := storage.GetWorkflowInstanceHistory(ctx, &current, nil)
		if err != nil {
			return nil, ctxerrors.Wrap(err,
				"read workflow continuation history")
		}

		next, err := continuedWorkflowExecution(events)
		if err != nil {
			return nil, err
		}

		current.ExecutionID = next
	}

	return nil, ctxerrors.Wrapf(commerr.ErrInvalidState,
		"workflow continuation chain exceeds %d handoffs",
		WorkflowContinuationLimit)
}

func continuedWorkflowExecution(events []*history.Event) (string, error) {
	for _, event := range events {
		if event.Type != history.EventType_WorkflowExecutionContinuedAsNew {
			continue
		}

		value := event.Attributes

		attributes, ok := value.(*history.ExecutionContinuedAsNewAttributes)
		if ok && attributes != nil {
			id, err := uuid.Parse(attributes.ContinuedExecutionID)
			if err == nil && id != uuid.Nil {
				return attributes.ContinuedExecutionID, nil
			}
		}

		break
	}

	return "", ctxerrors.Wrap(commerr.ErrInvalidState,
		"workflow continuation has no valid successor")
}
