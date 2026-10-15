package agent

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (r *Runtime) prepareChildWorkflowInbox(
	ctx context.Context,
	input workflowRoundInput,
	conversation *workflowConversation,
	ledger *skillLedger,
) error {
	if ledger == nil {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"child inbox skill ledger")
	}

	if err := r.validateChildWorkflowInbox(ctx, input,
		conversation); err != nil {
		return ctxerrors.Wrap(err, "validate child inbox preparation")
	}

	return r.prepareRootWorkflowInbox(ctx, input.Execution, conversation,
		ledger)
}

func (r *Runtime) validateChildWorkflowInbox(
	ctx context.Context,
	input workflowRoundInput,
	conversation *workflowConversation,
) error {
	if conversation == nil || conversation.Child == nil {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"child inbox launch context")
	}

	if err := validateWorkflowChildConversation(*conversation,
		input); err != nil {
		return ctxerrors.Wrap(err, "validate child inbox round")
	}

	store, ok := r.store.(session.ExecutionStorage)
	if !ok {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"child inbox execution storage")
	}

	execution, err := store.GetExecution(ctx, input.Execution.SessionID,
		input.Execution.ExecutionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read child inbox execution")
	}

	if childInboxLaunchChanged(execution, conversation.Child) {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"child inbox execution belongs to another launch")
	}

	if childInboxUnavailable(execution.State) {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"child inbox execution is unavailable")
	}

	run, err := r.store.GetAgentRun(ctx, execution.SessionID,
		*execution.AgentRunID)
	if err != nil {
		return ctxerrors.Wrap(err, "read child inbox launch")
	}

	return conversation.Child.validateAgainst(run)
}

func childInboxLaunchChanged(
	execution *models.Execution,
	child *workflowChildBootstrap,
) bool {
	return invalidChildCompactionExecution(execution) ||
		*execution.AgentRunID != child.AgentRunID ||
		execution.RootTurnID != child.TurnID
}

func childInboxUnavailable(state models.ExecutionState) bool {
	return state == models.ExecutionStatePaused ||
		state == models.ExecutionStateCompleted ||
		state == models.ExecutionStateFailed ||
		state == models.ExecutionStateCancelled
}

func (r *Runtime) deliverChildWorkflowInbox(
	ctx context.Context,
	input workflowRoundInput,
	conversation workflowConversation,
) error {
	if err := r.validateChildWorkflowInbox(ctx, input,
		&conversation); err != nil {
		return ctxerrors.Wrap(err, "validate child inbox delivery")
	}

	store, ok := r.store.(session.ChildExecutionInboxStorage)
	if !ok {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"child inbox delivery storage")
	}

	value := ctx.Value(workflowActivityLeaseContextKey{})

	lease, ok := value.(session.ActivityLease)
	if !ok {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"child inbox delivery activity lease")
	}

	if err := r.validateChildInboxActivity(ctx, input, lease); err != nil {
		return ctxerrors.Wrap(err, "validate child inbox saved model input")
	}

	_, err := store.DeliverChildExecutionMessages(ctx,
		input.Execution.SessionID, lease, conversation.InboxIDs)
	if err != nil {
		return ctxerrors.Wrap(err, "checkpoint child inbox delivery")
	}

	return nil
}

func (r *Runtime) validateChildInboxActivity(
	ctx context.Context,
	input workflowRoundInput,
	lease session.ActivityLease,
) error {
	store, ok := r.store.(session.ExecutionStorage)
	if !ok {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"child inbox activity storage")
	}

	activity, err := store.GetExecutionActivity(ctx, input.Execution.SessionID,
		lease.ID)
	if err != nil {
		return ctxerrors.Wrap(err, "read child inbox model activity")
	}

	encoded, err := json.Marshal(input)
	if err != nil {
		return ctxerrors.Wrap(err, "encode child inbox model input")
	}

	if activity.ExecutionID != input.Execution.ExecutionID ||
		!bytes.Equal(encoded, []byte(activity.InputJSON)) {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"child inbox input differs from its model activity")
	}

	return nil
}
