package protocol

import (
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

func (c *Controller) handleExecutionCall(
	ctx context.Context,
	payload json.RawMessage,
) (any, error) {
	request, err := decode[InputRequest[executionRequest]](payload)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "decode execution store request")
	}

	if err := c.assertSession(request.SessionID); err != nil {
		return nil, ctxerrors.Wrap(err, "authorize execution store session")
	}

	if err := c.requireCurrentWorkflowWorker(ctx); err != nil {
		return nil, ctxerrors.Wrap(err, "authorize execution store generation")
	}

	return c.callExecutionStore(ctx, request.Input)
}

func (c *Controller) requireCurrentWorkflowWorker(ctx context.Context) error {
	if err := c.requireRecoveryWorker(ctx, c.sessionID); err != nil {
		return ctxerrors.Wrap(err, "authorize workflow worker state")
	}

	current, err := c.store.CurrentWorkerGeneration(ctx, c.sessionID)
	if err != nil {
		return ctxerrors.Wrap(err, "find current workflow worker")
	}

	if current.ID != c.generationID {
		return ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"workflow worker was replaced")
	}

	return nil
}

func (c *Controller) callExecutionStore(
	ctx context.Context,
	r executionRequest,
) (any, error) {
	var (
		result any
		err    error
	)

	//nolint:exhaustive // Activity operations are dispatched separately.
	switch r.Operation {
	case executionCreate:
		result, err = c.store.CreateExecution(ctx, c.sessionID, r.Create)
	case executionGet:
		result, err = c.store.GetExecution(ctx, c.sessionID, r.ExecutionID)
	case executionSearch:
		result, err = c.store.SearchExecutions(ctx, c.sessionID, r.Search)
	case executionPause:
		result, err = c.store.PauseExecution(ctx, c.sessionID,
			r.ExecutionID, r.Reason)
	case executionResume:
		result, err = c.store.ResumeExecution(ctx, c.sessionID, r.ExecutionID)
	case executionResumeFromActivity, executionResolveFromActivity:
		result, err = c.callAgentExecutionControl(ctx, r)
	case executionEvents:
		result, err = c.store.ListExecutionEvents(ctx, c.sessionID,
			r.ExecutionID, r.Page)
	default:
		return c.callExecutionActivities(ctx, r)
	}

	if err != nil {
		return nil, ctxerrors.Wrap(err, "execution record operation")
	}

	return result, nil
}

func (c *Controller) callExecutionActivities(
	ctx context.Context,
	r executionRequest,
) (any, error) {
	if err := c.authorizeActivityGeneration(r); err != nil {
		return nil, err
	}

	var (
		result any
		err    error
	)

	//nolint:exhaustive // Record operations are dispatched separately.
	switch r.Operation {
	case executionBeginActivity:
		result, err = c.store.BeginExecutionActivity(ctx, c.sessionID,
			r.ExecutionID, r.GenerationID, r.Begin)
	case executionCompleteActivity, executionInterruptActivity:
		if r.Operation == executionCompleteActivity {
			result, err = c.completeExecutionActivity(ctx, r)
		} else {
			err = c.store.InterruptExecutionActivity(ctx, c.sessionID, r.Lease)
			result = struct{}{}
		}
	case executionResolveActivity:
		result, err = c.store.ResolveExecutionActivity(ctx, c.sessionID,
			r.ActivityID, r.Resolve)
	case executionGetActivity:
		result, err = c.store.GetExecutionActivity(ctx, c.sessionID,
			r.ActivityID)
	case executionListActivities:
		result, err = c.store.ListExecutionActivities(ctx, c.sessionID,
			r.ExecutionID, r.Page)
	default:
		return c.callExecutionTurn(ctx, r)
	}

	if err != nil {
		return nil, ctxerrors.Wrap(err, "execution activity operation")
	}

	return result, nil
}

func (c *Controller) completeExecutionActivity(
	ctx context.Context,
	r executionRequest,
) (any, error) {
	receipt, err := c.store.CompleteExecutionActivityReceipt(ctx, c.sessionID,
		r.Lease, r.Complete)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "commit worker activity result")
	}

	return receipt.Activity, nil
}

func (c *Controller) authorizeActivityGeneration(r executionRequest) error {
	generationID := c.generationID

	//nolint:exhaustive // Only claims and lease writes carry a generation.
	switch r.Operation {
	case executionBeginActivity:
		generationID = r.GenerationID
	case executionCompleteActivity, executionInterruptActivity,
		executionReserveCompletion:
		generationID = r.Lease.GenerationID
	}

	if generationID != c.generationID {
		return ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"activity generation differs from its connection")
	}

	return nil
}
