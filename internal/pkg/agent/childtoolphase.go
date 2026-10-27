package agent

import (
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (c *childWorkflowCallbacks) toolPhase(
	ctx context.Context,
	input workflowToolPhaseInput,
) (workflowToolPhaseState, error) {
	lease, err := c.runtime.validateWorkflowPhaseActivity(ctx, input,
		c.generationID)
	if err != nil {
		return input.State, ctxerrors.Wrap(err, "validate child tool phase")
	}

	shared, admitted, release, err := c.acquire(ctx, input.Tool.Round, false)
	if err != nil {
		return input.State, ctxerrors.Wrap(err, "admit child tool phase")
	}
	defer release()

	actor := *shared.actor

	actor.sink, err = newChildPhaseSink(admitted, actor.sink, lease.ID,
		shared.revision)
	if err != nil {
		return input.State, ctxerrors.Wrap(err, "buffer child phase records")
	}

	input.State.ChildProjection = nil

	if input.Phase == workflowToolPhaseQuestion {
		return c.prepareChildQuestionDelivery(admitted, &actor, input)
	}

	prepared, err := c.restoreChildToolPhase(admitted, shared.actor, input)
	if err != nil {
		return input.State, ctxerrors.Wrap(err, "restore private child scope")
	}

	if err := actor.recordToolPhaseStart(admitted, input); err != nil {
		return input.State, ctxerrors.Wrap(err, "record child phase start")
	}

	counter := modelTokenCounter(prepared.scope.options.deps.model)

	state, err := runDirectoryWorkflowToolPhase(admitted, prepared.scope, input,
		workflowToolOptions{
			Timeout:         c.runtime.toolTimeout,
			MaxResultTokens: c.runtime.maxToolResultTokens,
			Counter:         counter,
		})
	if err != nil {
		return state, ctxerrors.Wrap(err, "execute child tool phase")
	}

	if err := c.finishToolPhase(admitted, &actor, prepared.scope,
		input, &state); err != nil {
		return state, ctxerrors.Wrap(err, "finish child tool phase")
	}

	state.ChildProjection = actor.sink.phaseBuffer

	return state, nil
}

func (actor *childWorkflowActor) recordToolPhaseStart(
	ctx context.Context,
	input workflowToolPhaseInput,
) error {
	if input.Phase != workflowToolPhasePre {
		return nil
	}

	if err := actor.sink.onToolCallStart(ctx,
		workflowPhaseCallEvent(input.Tool)); err != nil {
		return ctxerrors.Wrap(err, "buffer child phase start")
	}

	return nil
}

func (c *childWorkflowCallbacks) restoreChildToolPhase(
	ctx context.Context,
	actor *childWorkflowActor,
	input workflowToolPhaseInput,
) (*preparedChildConversation, error) {
	var conversation workflowConversation
	if err := json.Unmarshal(input.Tool.Round.Execution.Context,
		&conversation); err != nil {
		return nil, ctxerrors.Wrap(err, "decode child phase context")
	}

	_, prepared, err := c.runtime.restoreChildWorkflowScope(ctx,
		actor.deps, actor.run, conversation)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "restore private child phase")
	}

	return prepared, nil
}

func (c *childWorkflowCallbacks) finishToolPhase(
	ctx context.Context,
	actor *childWorkflowActor,
	scope *directoryScope,
	input workflowToolPhaseInput,
	state *workflowToolPhaseState,
) error {
	if err := actor.checkpointWorkflowQuestion(ctx, c.runtime, input,
		state); err != nil {
		return ctxerrors.Wrap(err, "checkpoint child question")
	}

	if input.Phase != workflowToolPhaseProject {
		return nil
	}

	event := workflowPhaseCallEvent(input.Tool)
	event.Result = &state.Outcome.Result

	current := scope.context()

	directory, prompt := "", ""
	if !event.Result.IsError {
		directory, prompt = current.executor.Workspace(), current.prompt
	}

	if err := actor.sink.persistToolResult(ctx, event,
		directory, prompt); err != nil {
		return ctxerrors.Wrap(err, "record child phase result")
	}

	for _, injection := range state.Outcome.Injections {
		if err := actor.sink.onMessageInjection(ctx, injection); err != nil {
			return ctxerrors.Wrap(err, "record child phase injection")
		}
	}

	encoded, err := rootWorkflowToolContext(input.Tool, scope)
	if err != nil {
		return ctxerrors.Wrap(err, "retain projected child scope")
	}

	state.Outcome.Context = encoded

	return nil
}

func (c *childWorkflowCallbacks) refreshToolPhaseDirectory(
	ctx context.Context,
	actor *childWorkflowActor,
	input workflowToolPhaseInput,
	state *workflowToolPhaseState,
) error {
	if input.Phase != workflowToolPhasePost ||
		input.Tool.Call.Name != toolNameChangeDirectory ||
		state.Outcome.Result.IsError || state.Outcome.Failure != "" {
		return nil
	}

	store, ok := c.runtime.store.(session.ExecutionStorage)
	if !ok {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"child phase directory storage")
	}

	var conversation workflowConversation
	if err := json.Unmarshal(workflowToolPhaseTurnContext(input, *state),
		&conversation); err != nil {
		return ctxerrors.Wrap(err, "decode committed child directory")
	}

	if conversation.Runtime == nil {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"committed child directory state")
	}

	if err := refreshChildCallbackInbox(ctx, actor.run.inbox, store,
		conversation.Runtime.Directory.Directory); err != nil {
		return ctxerrors.Wrap(err, "refresh child phase inbox")
	}

	return nil
}
