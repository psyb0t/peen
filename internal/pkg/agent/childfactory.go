package agent

import (
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

type childWorkflowActor struct {
	run      *AgentRun
	sink     *agentRunSink
	deps     *launchAgentDeps
	prepared *preparedChildConversation
}

func (r *Runtime) restoreChildWorkflowActor(
	ctx context.Context,
	input workflowRoundInput,
	cancel context.CancelFunc,
	liveSink EventSink,
) (*childWorkflowActor, error) {
	if cancel == nil {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"child callback cancellation")
	}

	var conversation workflowConversation
	if err := json.Unmarshal(input.Execution.Context,
		&conversation); err != nil {
		return nil, ctxerrors.Wrap(err, "decode child callback context")
	}

	if err := r.validateChildWorkflowInbox(ctx, input,
		&conversation); err != nil {
		return nil, ctxerrors.Wrap(err, "validate child callback identity")
	}

	run, err := r.restoreChildCallbackRun(ctx, conversation.Child, cancel)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "restore child callback actor")
	}

	seed := &launchAgentDeps{
		sessionID: run.SessionID, parentTurnID: run.ParentTurnID,
		requestID: run.RequestID, liveSink: liveSink,
	}

	deps, prepared, err := r.restoreChildWorkflowScope(ctx, seed, run,
		conversation)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "restore child callback scope")
	}

	sink := newAgentRunSink(r.store, run.SessionID, run, liveSink,
		conversation.ModelReference)
	if err := r.bindChildCallbackInbox(ctx, input, run, sink,
		prepared); err != nil {
		return nil, ctxerrors.Wrap(err, "restore child callback inbox")
	}

	return &childWorkflowActor{
		run: run, sink: sink, deps: deps, prepared: prepared,
	}, nil
}

func (r *Runtime) restoreChildCallbackRun(
	ctx context.Context,
	saved *workflowChildBootstrap,
	cancel context.CancelFunc,
) (*AgentRun, error) {
	stored, err := r.store.GetAgentRun(ctx, saved.SessionID, saved.AgentRunID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read callback child run")
	}

	// Keep the actor private until its scope and inbox are fully bound.
	registry, err := NewAgentRunRegistry(saved.SessionID, nil, r.agentLimits)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build callback child registry")
	}

	run, err := registry.restoreWorkflowRun(stored, cancel, nil)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "hydrate callback child run")
	}

	return run, nil
}

func (r *Runtime) bindChildCallbackInbox(
	ctx context.Context,
	input workflowRoundInput,
	run *AgentRun,
	sink *agentRunSink,
	prepared *preparedChildConversation,
) error {
	inbox, err := newAgentInbox(sink, r.maxQueuedUserMessages)
	if err != nil {
		return ctxerrors.Wrap(err, "create callback child inbox")
	}

	if err := inbox.bindWorkflow(ctx, input.Execution.ExecutionID,
		prepared.scope.context().executor.Workspace()); err != nil {
		return ctxerrors.Wrap(err, "bind callback child inbox")
	}

	run.inbox = inbox

	return nil
}
