package agent

import (
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
)

func (c *rootWorkflowCallbacks) compactionHook(ctx context.Context,
	input workflowCompactionHookInput,
) (workflowCompactionHookResult, error) {
	shared, release, err := c.acquire(ctx, input.Round.Execution, true)
	if err != nil {
		return workflowCompactionHookResult{}, ctxerrors.Wrap(err,
			"admit root compaction hook")
	}
	defer release()

	if err := c.refresh(ctx, shared, input.Round.Execution); err != nil {
		return workflowCompactionHookResult{}, ctxerrors.Wrap(err,
			"refresh root compaction hook")
	}

	hook := composeCompactionHooks(c.runtime.compactionOptions.Hook,
		shared.prepared.runCompactionHook)

	return c.runtime.runWorkflowCompactionHook(ctx, shared.prepared.workspace,
		input, hook)
}

func (c *childWorkflowCallbacks) compactionHook(ctx context.Context,
	input workflowCompactionHookInput,
) (workflowCompactionHookResult, error) {
	shared, admitted, release, err := c.acquire(ctx, input.Round, true)
	if err != nil {
		return workflowCompactionHookResult{}, ctxerrors.Wrap(err,
			"admit child compaction hook")
	}
	defer release()

	if err := c.refresh(admitted, shared, input.Round); err != nil {
		return workflowCompactionHookResult{}, ctxerrors.Wrap(err,
			"refresh child compaction hook")
	}

	scope := shared.actor.prepared.scope
	hook := composeCompactionHooks(c.runtime.compactionOptions.Hook,
		scope.childCompactionHook(shared.actor.run))

	return c.runtime.runWorkflowCompactionHook(admitted,
		scope.context().executor.Workspace(), input, hook)
}

func (r *Runtime) runWorkflowCompactionHook(ctx context.Context,
	workspace string, input workflowCompactionHookInput, hook compactionHook,
) (workflowCompactionHookResult, error) {
	result := workflowCompactionHookResult{Payload: input.Payload}
	if result.Payload == nil {
		payload, err := r.workflowCompactionHookPayload(workspace, input.Round)
		if err != nil {
			return result, ctxerrors.Wrap(err,
				"prepare compaction hook payload")
		}

		result.Payload = payload
	}

	if result.Payload == nil || hook == nil {
		return result, nil
	}

	if err := hook(ctx, input.Event, *result.Payload); err != nil {
		if ctx.Err() != nil {
			return result, ctxerrors.Wrap(err, "interrupt compaction hook")
		}

		result.Failure = toolErrorMessage(err)
	}

	return result, nil
}

func (r *Runtime) workflowCompactionHookPayload(workspace string,
	input workflowRoundInput,
) (*compactionHookPayload, error) {
	units, err := selectWorkflowCompactionPrefix(r.models, input)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "select compaction hook prefix")
	}

	if units == 0 {
		//nolint:nilnil // An under-budget round has no compaction hook.
		return nil, nil
	}

	var conversation workflowConversation
	if err := json.Unmarshal(input.Execution.Context,
		&conversation); err != nil {
		return nil, ctxerrors.Wrap(err, "decode compaction hook conversation")
	}

	model, err := r.models.ResolveModel(conversation.ModelReference)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resolve compaction hook counter")
	}

	tools := make([]elelem.Tool, 0, len(conversation.Tools))
	for _, definition := range conversation.Tools {
		tools = append(tools, elelem.Tool{
			Name: definition.Name, Description: definition.Description,
			ArgumentsSchema: definition.ArgumentsSchema,
			StrictArguments: definition.StrictArguments,
		})
	}

	estimated, err := modelTokenCounter(model).Count(
		workflowCompactionMessages(conversation), tools)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "count compaction hook context")
	}

	built := compactor{
		options: compactionOptions{Workspace: workspace},
		plan:    conversation.Compaction.Plan,
	}
	payload := built.hookPayload(&elelem.TokenLimitEvent{
		EstimatedTokens: estimated, BudgetTokens: conversation.MaxContextTokens,
		Round: input.Round,
	}, units, built.plan.messageCount(units), 0)

	return &payload, nil
}
