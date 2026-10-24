package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/go-workflows/client"
	"github.com/psyb0t/go-workflows/workflow"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

type rootWorkflowSubmission struct {
	Instance *workflow.Instance
	Input    workflowExecutionInput
}

func (d *workflowDispatch) submitRoot(
	ctx context.Context,
	api *client.Client,
	prepared *preparedTurn,
) (rootWorkflowSubmission, error) {
	if api == nil || prepared == nil {
		return rootWorkflowSubmission{}, ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet, "root workflow submission")
	}

	if prepared.lease.SessionID != d.runtime.sessionID {
		return rootWorkflowSubmission{}, ctxerrors.Wrap(
			commerr.ErrPermissionDenied,
			"submitted root session differs")
	}

	input, err := d.prepareRootSubmission(ctx, prepared)
	if err != nil {
		return rootWorkflowSubmission{Input: input}, ctxerrors.Wrap(err,
			"prepare native root submission")
	}

	return d.submitRootWorkflow(ctx, api, input)
}

func (d *workflowDispatch) submitRootWorkflow(
	ctx context.Context,
	api *client.Client,
	input workflowExecutionInput,
) (rootWorkflowSubmission, error) {
	submission := rootWorkflowSubmission{Input: input}
	if input.SessionID != d.runtime.sessionID {
		return submission, ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"root workflow submission belongs to another session")
	}

	store, ok := d.runtime.store.(session.ExecutionStorage)
	if !ok {
		return submission, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"root workflow submission storage")
	}

	execution, err := store.GetExecution(ctx, input.SessionID,
		input.ExecutionID)
	if err != nil {
		return submission, ctxerrors.Wrap(err,
			"read submitted root workflow version")
	}

	if execution.Kind != models.ExecutionKindRoot {
		return submission, ctxerrors.Wrap(commerr.ErrConflict,
			"root workflow submission is not a root execution")
	}

	saved, err := retainedWorkflowSubmission(execution)
	if err != nil {
		return submission, ctxerrors.Wrap(err,
			"restore root submission arguments")
	}

	submission.Input = saved

	submission.Instance, err = api.CreateWorkflowInstance(ctx,
		client.WorkflowInstanceOptions{
			InstanceID: fmt.Sprintf("%s/%s", input.SessionID,
				input.ExecutionID),
			Queue: workflow.Queue(input.SessionID.String()),
		}, execution.WorkflowVersion, saved)
	if err != nil {
		return submission, ctxerrors.Wrap(err, "submit native root workflow")
	}

	return submission, nil
}

func (d *workflowDispatch) prepareRootSubmission(
	ctx context.Context,
	prepared *preparedTurn,
) (workflowExecutionInput, error) {
	store, ok := d.runtime.store.(session.ExecutionStorage)
	if !ok {
		return workflowExecutionInput{}, ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet, "root submission execution storage")
	}

	scope, err := d.runtime.rootDirectoryScope(ctx, prepared)
	if err != nil {
		return workflowExecutionInput{}, ctxerrors.Wrap(err,
			"assemble submitted root scope")
	}

	conversation, err := d.runtime.rootWorkflowConversation(prepared, scope)
	if err != nil {
		return workflowExecutionInput{}, ctxerrors.Wrap(err,
			"snapshot submitted root conversation")
	}

	encoded, err := json.Marshal(conversation)
	if err != nil {
		return workflowExecutionInput{}, ctxerrors.Wrap(err,
			"encode submitted root conversation")
	}

	executionID := uuid.New()
	instance := nativeWorkflowInstance(prepared.lease.SessionID, executionID)

	execution, err := store.CreateExecution(ctx, prepared.lease.SessionID,
		session.CreateExecutionInput{
			DeferredInboxID: prepared.deferredInboxID,
			ID:              executionID, RootTurnID: prepared.lease.TurnID,
			WorkflowExecutionID: instance.ExecutionID,
			WorkflowVersion:     agentWorkflowName,
			Task:                prepared.input.Message,
			WorkingDirectory:    prepared.workspace,
			Context:             encoded,
		})
	if err != nil {
		return workflowExecutionInput{}, ctxerrors.Wrap(err,
			"create submitted root execution")
	}

	input := workflowExecutionInput{
		SessionID: execution.SessionID, ExecutionID: execution.ID,
		MaxRounds:          d.runtime.maxToolRounds,
		MaxConcurrentTools: d.runtime.maxConcurrentTools, Context: encoded,
	}
	if err := d.bindSubmittedRoot(ctx, store, prepared, scope,
		input); err != nil {
		return input, ctxerrors.Wrap(err, "bind submitted root callbacks")
	}

	return input, nil
}

func (d *workflowDispatch) bindSubmittedRoot(
	ctx context.Context,
	store session.ExecutionStorage,
	prepared *preparedTurn,
	scope *directoryScope,
	input workflowExecutionInput,
) error {
	lease, err := store.RestoreExecutionTurn(ctx, input.SessionID,
		input.ExecutionID, session.RestoreExecutionTurnInput{
			OwnerGenerationID:  d.root.generationID,
			WorkerGenerationID: d.runtime.workerGenerationID,
		})
	if err != nil {
		return ctxerrors.Wrap(err, "adopt submitted root holder")
	}

	if lease != prepared.lease {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"submitted root lease changed")
	}

	if err := d.runtime.bindRootWorkflowInbox(ctx, prepared,
		input.ExecutionID); err != nil {
		return ctxerrors.Wrap(err, "bind submitted root inbox")
	}

	slot := d.root.slot(input.ExecutionID)

	release, err := slot.gate.acquire(ctx, true)
	if err != nil {
		return ctxerrors.Wrap(err, "admit submitted root cache")
	}
	defer release()

	slot.current = &rootCallbackScope{
		prepared: prepared, directory: scope, lease: lease,
		releaseTurnSlot: prepared.releaseTurnSlot,
	}

	return nil
}
