package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// The wrapper checkpoints the proposed summary without publishing it into
// the transcript. Hooks run only after summarization succeeds.
type workflowPreparedCompaction struct {
	Result workflowCompactionResult `json:"result"`
}

func (e *workflowExecution) prepareCompaction(
	ctx context.Context,
	input workflowRoundInput,
) (workflowActivityResult[workflowPreparedCompaction], error) {
	return runWorkflowCheckpoint(ctx, e, input.Execution,
		session.ScheduleActivityInput{
			StepID: fmt.Sprintf("compactPrepare:%d", input.Round),
			Kind:   models.ActivityKindCompaction, Name: compactionActivityName,
			ReplayPolicy: models.ActivityReplayPolicyRetry,
		}, input, func(ctx context.Context) (
			workflowPreparedCompaction, error,
		) {
			result := workflowCompactionResult{Context: input.Execution.Context}

			if e.options.CompactRound != nil {
				var err error

				result, err = e.options.CompactRound(ctx, input)
				if err != nil {
					return workflowPreparedCompaction{}, ctxerrors.Wrap(err,
						"prepare summary before compaction hooks")
				}
			}

			return workflowPreparedCompaction{Result: result}, nil
		}, func(workflowPreparedCompaction) json.RawMessage {
			return input.Execution.Context
		})
}

func (e *workflowExecution) commitPreparedCompaction(
	ctx context.Context,
	input workflowRoundInput,
	prepared workflowCompactionResult,
) (workflowActivityResult[workflowCompactionResult], error) {
	return runWorkflowCheckpoint(ctx, e, input.Execution,
		session.ScheduleActivityInput{
			StepID: fmt.Sprintf("compact:%d", input.Round),
			Kind:   models.ActivityKindCompaction, Name: compactionActivityName,
			ReplayPolicy: models.ActivityReplayPolicyRetry,
		}, input, func(ctx context.Context) (workflowCompactionResult, error) {
			if prepared.Compaction == nil {
				return prepared, nil
			}

			lease, err := rootCompactionLease(ctx)
			if err != nil {
				return prepared, ctxerrors.Wrap(err,
					"read summary commit lease")
			}

			var conversation workflowConversation

			err = json.Unmarshal(prepared.Context, &conversation)
			if err != nil {
				return prepared, ctxerrors.Wrap(err, "decode prepared summary")
			}

			if conversation.Compaction == nil ||
				conversation.Compaction.Head == nil ||
				conversation.Compaction.Head.ID != prepared.Compaction.ID {
				return prepared, ctxerrors.Wrap(commerr.ErrConflict,
					"prepared summary identity does not match context")
			}

			compaction := *prepared.Compaction
			compaction.ID = lease.ID
			conversation.Compaction.Head.ID = lease.ID

			encoded, err := json.Marshal(conversation)
			if err != nil {
				return prepared, ctxerrors.Wrap(err, "encode summary identity")
			}

			return workflowCompactionResult{
				Context: encoded, Compaction: &compaction,
			}, nil
		}, func(result workflowCompactionResult) json.RawMessage {
			return result.Context
		})
}
