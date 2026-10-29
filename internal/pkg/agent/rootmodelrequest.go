package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

type rootWorkflowModelAuditSettings struct {
	modelAuditRequestSettings
	ExecutionID        uuid.UUID `json:"executionId"`
	WorkflowRound      int       `json:"workflowRound"`
	ActivityID         uuid.UUID `json:"activityId"`
	Attempt            int64     `json:"attempt"`
	WorkerGenerationID uuid.UUID `json:"workerGenerationId"`
}

func (p *preparedTurn) bindRootModelCallbacks(
	request *elelem.Request,
	audit *modelAuditRecorder,
) *elelem.Request {
	return p.bindRootModelCallbacksWithAssistant(request, audit, p)
}

func (p *preparedTurn) bindRootModelCallbacksWithAssistant(
	request *elelem.Request,
	audit *modelAuditRecorder,
	assistant *preparedTurn,
) *elelem.Request {
	// Held stream deltas reach the sink before the message is kept, so a
	// sink that refuses one stops the round before the reply is stored.
	request.OnRoundStart(p.rootRoundStartCallback(audit)).
		OnRoundEnd(audit.onRoundEnd).
		OnRetry(p.rootRetryCallback(audit)).
		OnAssistantMessage(func(ctx context.Context, _ elelem.Message) error {
			return p.turn.publishHeldDeltas(ctx)
		}).
		OnAssistantMessage(assistant.rootAssistantMessageCallback(audit))

	return p.adapter.Bind(request)
}

// Each workflow attempt is an audited provider invocation with saved input.
// Tool handlers are executed by their own activities.
func (r *Runtime) workflowRootModelRound(
	ctx context.Context,
	prepared *preparedTurn,
	input workflowRoundInput,
) (workflowRoundResult, error) {
	return r.workflowRootModelRoundWithAssistant(ctx, prepared, prepared, input)
}

func (r *Runtime) workflowRootModelRoundWithAssistant(
	ctx context.Context,
	prepared, assistant *preparedTurn,
	input workflowRoundInput,
) (workflowRoundResult, error) {
	request, conversation, err := r.rootWorkflowModelRequest(ctx,
		prepared, input)
	if err != nil {
		return workflowRoundResult{}, ctxerrors.Wrap(err,
			"build root workflow model request")
	}

	projection, err := prepared.restoreWorkflowModelStream(ctx)
	if err != nil {
		return workflowRoundResult{}, ctxerrors.Wrap(err,
			"restore root model stream")
	}

	audit, err := r.rootWorkflowModelAudit(ctx, prepared, input, conversation)
	if err != nil {
		return workflowRoundResult{}, ctxerrors.Wrap(err,
			"start root workflow model audit")
	}

	monitor := rootWorkflowModelMonitor{startedAt: time.Now()}
	request = monitor.bind(request, prepared, assistant, audit, input.Round).
		WithTimeout(r.modelRequestTimeout)

	response, err := runWorkflowModelResponse(ctx, request)

	err = monitor.closeInterruptedStream(ctx, prepared, err)
	if auditErr := audit.finish(context.WithoutCancel(ctx), response,
		err); auditErr != nil {
		err = errors.Join(err, auditErr)
	}

	observeModelRequest(r.metrics, modelMetricStageTurn,
		modelMetricFunctionRunProvider, conversation.ModelReference,
		monitor.startedAt, monitor.firstDeltaAt, response, err)

	if err != nil {
		return workflowRoundResult{}, ctxerrors.Wrap(err,
			"run root workflow model round")
	}

	conversation.Stream = &workflowToolStream{
		Base: projection.nextIndex(), Count: len(response.ToolCalls),
	}

	result, err := workflowModelResult(ctx, response, conversation)
	if err != nil {
		return workflowRoundResult{}, ctxerrors.Wrap(err,
			"save root model context")
	}

	return prepared.withWorkflowInboxContinuation(result), nil
}

func (r *Runtime) rootWorkflowModelRequest(
	ctx context.Context,
	prepared *preparedTurn,
	input workflowRoundInput,
) (*elelem.Request, workflowConversation, error) {
	if err := r.validateRootWorkflowModelInput(ctx,
		prepared, input); err != nil {
		return nil, workflowConversation{}, err
	}

	request, conversation, err := workflowModelRequest(r.models, input)
	if err != nil {
		return nil, conversation, ctxerrors.Wrap(err,
			"build saved model request")
	}

	if conversation.ModelReference != prepared.modelReference {
		return nil, conversation, ctxerrors.Wrap(commerr.ErrConflict,
			"workflow model differs from its root turn")
	}

	if err := r.deliverRootWorkflowInbox(ctx, prepared, input.Execution,
		conversation.InboxIDs); err != nil {
		return nil, conversation, ctxerrors.Wrap(err,
			"deliver root workflow inbox")
	}

	return request, conversation, nil
}

func (r *Runtime) rootWorkflowModelAudit(
	ctx context.Context,
	prepared *preparedTurn,
	input workflowRoundInput,
	conversation workflowConversation,
) (*modelAuditRecorder, error) {
	settings, err := newModelAuditSettings(prepared.model.Model,
		false, workflowManualRequestRoundLimit, conversation.MaxContextTokens,
		0, r.maxConcurrentTools,
		r.toolTimeout, r.maxToolResultTokens, r.modelRequestTimeout)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode workflow model audit settings")
	}

	encoded, err := rootWorkflowAuditSettings(ctx, settings,
		input, conversation.Streaming)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "link workflow model audit attempt")
	}

	return newModelAuditRecorder(ctx, modelAuditOptions{
		Store: r.store, SessionID: prepared.opened.Session.ID,
		TurnID: prepared.lease.TurnID, Stage: models.ModelRunStageTurn,
		ModelReference: prepared.modelReference, Model: prepared.model.Model,
		RequestSettingsJSON: encoded, Now: r.now,
	})
}

func rootWorkflowAuditSettings(
	ctx context.Context,
	settings string,
	input workflowRoundInput,
	streaming *bool,
) (string, error) {
	value := ctx.Value(workflowActivityLeaseContextKey{})

	lease, ok := value.(session.ActivityLease)
	if !ok {
		return "", ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"workflow audit requires an activity lease")
	}

	audit := rootWorkflowModelAuditSettings{
		ExecutionID: input.Execution.ExecutionID, WorkflowRound: input.Round,
		ActivityID: lease.ID, Attempt: lease.Attempt,
		WorkerGenerationID: lease.GenerationID,
	}
	if err := json.Unmarshal([]byte(settings),
		&audit.modelAuditRequestSettings); err != nil {
		return "", ctxerrors.Wrap(err, "decode workflow audit settings")
	}

	if streaming != nil {
		audit.Streaming = *streaming
	}

	encoded, err := json.Marshal(audit)
	if err != nil {
		return "", ctxerrors.Wrap(err, "encode workflow audit attempt")
	}

	return string(encoded), nil
}

func (r *Runtime) validateRootWorkflowModelInput(
	ctx context.Context,
	prepared *preparedTurn,
	input workflowRoundInput,
) error {
	if !validWorkflowExecutionInput(input.Execution) ||
		input.Round < 0 || input.Round >= input.Execution.MaxRounds {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"root workflow round input")
	}

	if input.Execution.SessionID != prepared.opened.Session.ID {
		return ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"workflow belongs to another root session")
	}

	store, ok := r.store.(session.ExecutionStorage)
	if !ok {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"root workflow requires execution storage")
	}

	execution, err := store.GetExecution(ctx,
		input.Execution.SessionID, input.Execution.ExecutionID)
	if err != nil {
		return ctxerrors.Wrap(err, "find root workflow execution")
	}

	if execution.RootTurnID != prepared.lease.TurnID ||
		execution.Kind != models.ExecutionKindRoot {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"workflow belongs to another root turn")
	}

	return nil
}
