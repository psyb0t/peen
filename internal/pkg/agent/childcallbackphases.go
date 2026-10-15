package agent

import (
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

func (c *childWorkflowCallbacks) bind(options *workflowExecutionOptions) {
	c.generationID = options.GenerationID
	options.PrepareRound = c.prepareRound
	options.CompactRound = c.compactRound
	options.CompactionHook = c.compactionHook
	options.ModelRound = c.modelRound
	options.AfterModelRound = c.afterModelRound
	options.ToolCall = c.toolCall
	options.ToolPhase = c.toolPhase
	options.AfterToolPhase = c.afterToolPhase
	options.BeforeFinish = c.beforeFinish
	options.AfterFinish = c.afterFinish
}

func (c *childWorkflowCallbacks) prepareRound(
	ctx context.Context,
	input workflowRoundInput,
) (json.RawMessage, error) {
	shared, admitted, release, err := c.acquire(ctx, input, true)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "admit child preparation")
	}
	defer release()

	if err := c.refresh(admitted, shared, input); err != nil {
		return nil, ctxerrors.Wrap(err, "refresh child preparation")
	}

	return c.runtime.prepareChildWorkflowRound(admitted,
		shared.actor.prepared.scope, input)
}

func (c *childWorkflowCallbacks) modelRound(
	ctx context.Context,
	input workflowRoundInput,
) (workflowRoundResult, error) {
	shared, admitted, release, err := c.acquire(ctx, input, true)
	if err != nil {
		return workflowRoundResult{}, ctxerrors.Wrap(err, "admit child model")
	}
	defer release()

	if err := c.refresh(admitted, shared, input); err != nil {
		return workflowRoundResult{}, ctxerrors.Wrap(err, "refresh child model")
	}

	assistant, err := c.childModelProjectionSink(admitted, shared, input)
	if err != nil {
		return workflowRoundResult{}, ctxerrors.Wrap(err,
			"buffer child model transcript")
	}

	return c.runtime.workflowChildModelRoundWithAssistant(admitted,
		shared.actor.sink, assistant, input)
}

func (c *childWorkflowCallbacks) toolCall(
	ctx context.Context,
	input workflowToolInput,
) (workflowToolResult, error) {
	shared, admitted, release, err := c.acquire(ctx, input.Round, false)
	if err != nil {
		return workflowToolResult{}, ctxerrors.Wrap(err, "admit child tool")
	}
	defer release()

	return c.runtime.workflowChildToolCall(admitted, shared.actor.sink,
		shared.actor.prepared.scope, input)
}

func (c *childWorkflowCallbacks) compactRound(
	ctx context.Context,
	input workflowRoundInput,
) (workflowCompactionResult, error) {
	_, admitted, release, err := c.acquire(ctx, input, true)
	if err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"admit child compaction")
	}
	defer release()

	return c.runtime.workflowChildCompactRound(admitted, input)
}

func (c *childWorkflowCallbacks) beforeFinish(
	ctx context.Context,
	input workflowFinishInput,
) (workflowFinishResult, error) {
	round := workflowRoundInput{Execution: input.Execution, Round: input.Round}
	// Failed workflows use MaxRounds as a terminal marker.
	if input.Result.Finish.State == models.ExecutionStateFailed &&
		input.Round == input.Execution.MaxRounds {
		round.Round--
	}
	// The schedule retains the round's starting input beside its final result.
	round.Execution.Context = input.Result.Result.Context

	_, admitted, release, err := c.acquire(ctx, round, true)
	if err != nil {
		return workflowFinishResult{}, ctxerrors.Wrap(err, "admit child finish")
	}
	defer release()

	final := input
	final.Execution = round.Execution

	return c.runtime.prepareChildWorkflowFinish(admitted, final)
}
