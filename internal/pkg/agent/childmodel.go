package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (r *Runtime) workflowChildModelRound(
	ctx context.Context,
	sink *agentRunSink,
	input workflowRoundInput,
) (workflowRoundResult, error) {
	return r.workflowChildModelRoundWithAssistant(ctx, sink, sink, input)
}

func (r *Runtime) workflowChildModelRoundWithAssistant(
	ctx context.Context,
	sink, assistant *agentRunSink,
	input workflowRoundInput,
) (workflowRoundResult, error) {
	request, conversation, err := r.childWorkflowModelRequest(ctx, sink, input)
	if err != nil {
		return workflowRoundResult{}, ctxerrors.Wrap(err,
			"build child workflow model request")
	}

	audit, err := r.childWorkflowModelAudit(ctx, input, conversation)
	if err != nil {
		return workflowRoundResult{}, ctxerrors.Wrap(err,
			"start child workflow model audit")
	}

	monitor := rootWorkflowModelMonitor{startedAt: time.Now()}
	bindChildWorkflowModel(request, sink, assistant, audit, &monitor)

	response, err := runWorkflowModelResponse(ctx, request)
	if auditErr := audit.finish(context.WithoutCancel(ctx), response,
		err); auditErr != nil {
		err = errors.Join(err, auditErr)
	}

	observeModelRequest(r.metrics, modelMetricStageChild,
		modelMetricFunctionRunChild, conversation.ModelReference,
		monitor.startedAt, monitor.firstDeltaAt, response, err)

	if err != nil {
		return workflowRoundResult{}, ctxerrors.Wrap(err,
			"run child workflow model round")
	}

	result, err := workflowModelResult(ctx, response, conversation)
	if err != nil {
		return result, ctxerrors.Wrap(err, "save child model context")
	}

	result.ChildProjection = assistant.phaseBuffer

	return r.childWorkflowInboxContinuation(ctx, input, result)
}

func (r *Runtime) childWorkflowInboxContinuation(
	ctx context.Context,
	input workflowRoundInput,
	result workflowRoundResult,
) (workflowRoundResult, error) {
	store, ok := r.store.(session.ExecutionStorage)
	if !ok {
		return result, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"child continuation inbox storage")
	}

	page, err := store.ListExecutionMessages(ctx, input.Execution.SessionID,
		input.Execution.ExecutionID, session.ListExecutionInboxOptions{
			State: models.InboxMessageStateQueued, Limit: 1,
		})
	if err != nil {
		return result, ctxerrors.Wrap(err, "read child continuation inbox")
	}

	result.Continue = len(page.Items) != 0

	return result, nil
}

func (r *Runtime) childWorkflowModelRequest(
	ctx context.Context,
	sink *agentRunSink,
	input workflowRoundInput,
) (*elelem.Request, workflowConversation, error) {
	var conversation workflowConversation
	if err := json.Unmarshal(input.Execution.Context,
		&conversation); err != nil {
		return nil, conversation, ctxerrors.Wrap(err,
			"decode child model input")
	}

	if err := r.validateChildWorkflowInbox(ctx, input,
		&conversation); err != nil {
		return nil, conversation, ctxerrors.Wrap(err,
			"validate child model actor")
	}

	if sink == nil || sink.run == nil ||
		sink.sessionID != conversation.Child.SessionID ||
		sink.run.ID != conversation.Child.AgentRunID {
		return nil, conversation, ctxerrors.Wrap(commerr.ErrConflict,
			"child model sink belongs to another actor")
	}

	request, saved, err := workflowModelRequest(r.models, input)
	if err != nil {
		return nil, saved, ctxerrors.Wrap(err,
			"build saved child model request")
	}

	if err := r.deliverChildWorkflowInbox(ctx, input, saved); err != nil {
		return nil, saved, ctxerrors.Wrap(err, "deliver saved child inbox")
	}

	if sink.run.inbox != nil {
		sink.run.inbox.markWorkflowDelivered(saved.InboxIDs)
	}

	return request, saved, nil
}

func (r *Runtime) childWorkflowModelAudit(
	ctx context.Context,
	input workflowRoundInput,
	conversation workflowConversation,
) (*modelAuditRecorder, error) {
	model, err := r.models.ResolveModel(conversation.ModelReference)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resolve child audit model")
	}

	model = retainedWorkflowModel(conversation, model)

	settings, err := newModelAuditSettings(model.Model,
		false, workflowManualRequestRoundLimit, conversation.MaxContextTokens,
		0, r.maxConcurrentTools, r.toolTimeout, r.maxToolResultTokens, 0)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode child workflow audit settings")
	}

	encoded, err := rootWorkflowAuditSettings(ctx, settings, input,
		conversation.Streaming)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "link child workflow audit attempt")
	}

	return newModelAuditRecorder(ctx, modelAuditOptions{
		Store: r.store, SessionID: conversation.Child.SessionID,
		TurnID:         conversation.Child.TurnID,
		AgentRunID:     &conversation.Child.AgentRunID,
		Stage:          models.ModelRunStageChild,
		ModelReference: conversation.ModelReference, Model: model.Model,
		RequestSettingsJSON: encoded, Now: r.now,
	})
}

func bindChildWorkflowModel(
	request *elelem.Request,
	sink, assistant *agentRunSink,
	audit *modelAuditRecorder,
	monitor *rootWorkflowModelMonitor,
) {
	request.OnRoundStart(audit.onRoundStart).
		OnRoundEnd(audit.onRoundEnd).
		OnAssistantMessage(childAssistantCallback(assistant, audit)).
		OnText(sink.onText).OnReasoning(sink.onReasoning).
		OnRetry(func(ctx context.Context, attempt elelem.RetryAttempt) error {
			if err := sink.onRetry(ctx, attempt); err != nil {
				return ctxerrors.Wrap(err, "record child workflow retry")
			}

			return audit.onRetry(ctx, attempt)
		}).OnDelta(func(context.Context, elelem.Delta) error {
		monitor.firstDeltaOnce.Do(func() { monitor.firstDeltaAt = time.Now() })

		return nil
	})
}
