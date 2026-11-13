package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/go-workflows/backend"
	"github.com/psyb0t/go-workflows/workflow"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

func (s *Store) resolveWorkflowExecution(
	ctx context.Context,
	execution *models.Execution,
) error {
	if s.workflowHandle.Workflows == nil ||
		execution.WorkflowExecutionID == "" {
		return nil
	}

	previous := execution.WorkflowExecutionID
	instanceID := fmt.Sprintf("%s/%s", execution.SessionID, execution.ID)
	initial := uuid.NewSHA1(uuid.NameSpaceOID, []byte(instanceID)).String()

	instance, err := db.ResolveWorkflowExecution(ctx,
		s.workflowHandle.Workflows, &workflow.Instance{
			InstanceID:  instanceID,
			ExecutionID: previous,
		})
	if errors.Is(err, backend.ErrInstanceNotFound) && previous == initial {
		// The record is written before its initial SDK submission.
		return nil
	}

	if errors.Is(err, backend.ErrInstanceNotFound) {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"cached workflow successor is missing")
	}

	if err != nil {
		return ctxerrors.Wrap(err, "resolve session workflow execution")
	}

	if instance.ExecutionID == previous {
		return nil
	}

	repo := s.query.Execution

	_, err = repo.WithContext(ctx).Where(
		repo.SessionID.Eq(execution.SessionID), repo.ID.Eq(execution.ID),
		repo.WorkflowExecutionID.Eq(previous),
	).UpdateColumnSimple(repo.WorkflowExecutionID.Value(instance.ExecutionID))
	if err != nil {
		ctxscope.GetLogger(ctx).Warn("workflow pointer repair failed",
			"execution_id", execution.ID, "err", err)
	}

	execution.WorkflowExecutionID = instance.ExecutionID

	return nil
}
