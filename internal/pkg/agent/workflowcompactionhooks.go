package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/hooks"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const compactionHookActivityName = "compactionHook"

type workflowCompactionHookInput struct {
	Round   workflowRoundInput     `json:"round"`
	Event   harness.HookEvent      `json:"event"`
	Payload *compactionHookPayload `json:"payload,omitempty"`
}

type workflowCompactionHookResult struct {
	Payload *compactionHookPayload `json:"payload,omitempty"`
	Failure string                 `json:"failure,omitempty"`
}

func (e *workflowExecution) compactWithHooks(ctx context.Context,
	input workflowRoundInput,
) (workflowActivityResult[workflowCompactionResult], error) {
	before, err := e.compactionHook(ctx, workflowCompactionHookInput{
		Round: input, Event: harness.HookEventPreCompact,
	})
	if err != nil {
		return workflowActivityResult[workflowCompactionResult]{}, err
	}

	if !compactionHookCompleted(before.Decision) {
		return workflowActivityResult[workflowCompactionResult]{
			Decision: before.Decision,
		}, nil
	}

	if before.Output.Failure != "" {
		return workflowActivityResult[workflowCompactionResult]{},
			ctxerrors.Wrap(hooks.ErrDenied, before.Output.Failure)
	}

	result, err := e.checkpointCompaction(ctx, input)
	if err != nil {
		return result, ctxerrors.Wrap(err, "checkpoint summary after pre hook")
	}

	if result.Output.Compaction == nil || before.Output.Payload == nil {
		return result, nil
	}

	return e.completeCompactionHooks(ctx, input, result, before.Output)
}

func (e *workflowExecution) completeCompactionHooks(ctx context.Context,
	input workflowRoundInput,
	result workflowActivityResult[workflowCompactionResult],
	before workflowCompactionHookResult,
) (workflowActivityResult[workflowCompactionResult], error) {
	payload := *before.Payload
	payload.SummaryTokens = result.Output.Compaction.SummaryTokenCount
	input.Execution.Context = result.Output.Context

	after, err := e.compactionHook(ctx, workflowCompactionHookInput{
		Round: input, Event: harness.HookEventPostCompact, Payload: &payload,
	})
	if err != nil {
		return result, ctxerrors.Wrap(err, "checkpoint post-compaction hook")
	}

	if !compactionHookCompleted(after.Decision) {
		result.Decision = after.Decision

		return result, nil
	}

	if after.Output.Failure != "" {
		ctxscope.GetLogger(ctx).Warn("post-compaction hook failed, continuing",
			"reason", "post_compaction_hook_failed",
			"err", ctxerrors.New(after.Output.Failure))
	}

	return result, nil
}

func compactionHookCompleted(decision session.ActivityDecision) bool {
	return decision == session.ActivityDecisionExecute ||
		decision == session.ActivityDecisionReuse
}

func (e *workflowExecution) compactionHook(ctx context.Context,
	input workflowCompactionHookInput,
) (workflowActivityResult[workflowCompactionHookResult], error) {
	if input.Round.Round < 0 ||
		input.Round.Round >= input.Round.Execution.MaxRounds ||
		(input.Event == harness.HookEventPostCompact && input.Payload == nil) {
		return workflowActivityResult[workflowCompactionHookResult]{},
			ctxerrors.Wrap(commerr.ErrValidationFailed, "compaction hook input")
	}

	if input.Event != harness.HookEventPreCompact &&
		input.Event != harness.HookEventPostCompact {
		return workflowActivityResult[workflowCompactionHookResult]{},
			ctxerrors.Wrap(commerr.ErrValidationFailed, "compaction hook event")
	}

	return runWorkflowCheckpoint(ctx, e, input.Round.Execution,
		session.ScheduleActivityInput{
			StepID: fmt.Sprintf("compactionHook:%d:%s", input.Round.Round,
				input.Event),
			Kind: models.ActivityKindTool, Name: compactionHookActivityName,
			ReplayPolicy: models.ActivityReplayPolicyReconcile,
		}, input, func(ctx context.Context) (
			workflowCompactionHookResult, error,
		) {
			return e.options.CompactionHook(ctx, input)
		}, func(workflowCompactionHookResult) json.RawMessage {
			return input.Round.Execution.Context
		})
}

func (d *workflowDispatch) compactionHook(ctx context.Context,
	input workflowCompactionHookInput,
) (workflowCompactionHookResult, error) {
	callbacks, err := d.callbacks(ctx, input.Round.Execution)
	if err != nil {
		return workflowCompactionHookResult{}, ctxerrors.Wrap(err,
			"dispatch compaction hook")
	}

	return callbacks.CompactionHook(ctx, input)
}
