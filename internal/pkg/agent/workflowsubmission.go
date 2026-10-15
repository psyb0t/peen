package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cschleiden/go-workflows/client"
	"github.com/cschleiden/go-workflows/workflow"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

type workflowExecutionLimits struct {
	MaxRounds          int `json:"maxRounds"`
	MaxConcurrentTools int `json:"maxConcurrentTools"`
}

func nativeWorkflowInstance(
	sessionID, executionID uuid.UUID,
) *workflow.Instance {
	instanceID := fmt.Sprintf("%s/%s", sessionID, executionID)

	return &workflow.Instance{
		InstanceID: instanceID,
		ExecutionID: uuid.NewSHA1(uuid.NameSpaceOID,
			[]byte(instanceID)).String(),
	}
}

func retainedWorkflowSubmission(
	execution *models.Execution,
) (workflowExecutionInput, error) {
	instance := nativeWorkflowInstance(execution.SessionID, execution.ID)
	if execution.WorkflowExecutionID != instance.ExecutionID {
		return workflowExecutionInput{}, ctxerrors.Wrap(commerr.ErrConflict,
			"saved workflow submission identity differs")
	}

	var conversation workflowConversation

	err := json.Unmarshal([]byte(execution.ContextJSON), &conversation)
	if err != nil {
		return workflowExecutionInput{}, ctxerrors.Wrap(err,
			"decode retained workflow submission")
	}

	limits := conversation.Limits
	if limits == nil || limits.MaxRounds < 1 || limits.MaxConcurrentTools < 1 {
		return workflowExecutionInput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"saved workflow submission limits")
	}

	return workflowExecutionInput{
		SessionID: execution.SessionID, ExecutionID: execution.ID,
		MaxRounds:          limits.MaxRounds,
		MaxConcurrentTools: limits.MaxConcurrentTools,
		Context:            json.RawMessage(execution.ContextJSON),
	}, nil
}

func (n *nativeWorkflowRunner) submitRetainedExecution(
	ctx context.Context,
	execution *models.Execution,
) error {
	// Re-read control before scheduling. Activities also fence a later Stop.
	current, err := n.engine.options.Store.GetExecution(ctx,
		execution.SessionID, execution.ID)
	if err != nil {
		return ctxerrors.Wrap(err,
			"read retained submission control")
	}

	if childInboxUnavailable(current.State) {
		if current.Kind == models.ExecutionKindChild {
			if err := n.reconcileSubmittedChild(ctx, current); err != nil {
				return ctxerrors.Wrap(err,
					"reconcile stopped child submission")
			}
		}

		return nil
	}

	input, err := retainedWorkflowSubmission(current)
	if err != nil {
		return ctxerrors.Wrap(err,
			"restore workflow submission arguments")
	}

	_, scheduleErr := n.api.CreateWorkflowInstance(ctx,
		client.WorkflowInstanceOptions{
			InstanceID: nativeWorkflowInstance(current.SessionID,
				current.ID).InstanceID,
			Queue: workflow.Queue(current.SessionID.String()),
		}, current.WorkflowVersion, input)

	cleanupErr := n.reconcileSubmittedChild(ctx, current)
	if err := errors.Join(scheduleErr, cleanupErr); err != nil {
		return ctxerrors.Wrap(err, "schedule retained workflow")
	}

	ctxscope.GetLogger(ctx).Info("workflow submission retained",
		"execution_id", current.ID, "session_id", current.SessionID)

	return nil
}
