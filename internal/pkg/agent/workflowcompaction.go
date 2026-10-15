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

const (
	compactionActivityName = "compactRoundV1"
)

type workflowCompactionResult struct {
	Context    json.RawMessage          `json:"context"`
	Compaction *session.CompactionInput `json:"compaction,omitempty"`
}

func (e *workflowExecution) compactRound(
	ctx context.Context,
	input workflowRoundInput,
) (workflowActivityResult[workflowCompactionResult], error) {
	if input.Round < 0 || input.Round >= input.Execution.MaxRounds {
		return workflowActivityResult[workflowCompactionResult]{},
			ctxerrors.Wrap(commerr.ErrValidationFailed,
				"compaction activity round")
	}

	if e.options.CompactionHook != nil {
		return e.compactWithHooks(ctx, input)
	}

	return e.checkpointCompaction(ctx, input)
}

func (e *workflowExecution) checkpointCompaction(
	ctx context.Context,
	input workflowRoundInput,
) (workflowActivityResult[workflowCompactionResult], error) {
	return runWorkflowCheckpoint(ctx, e, input.Execution,
		session.ScheduleActivityInput{
			StepID: fmt.Sprintf("compact:%d", input.Round),
			Kind:   models.ActivityKindCompaction, Name: compactionActivityName,
			ReplayPolicy: models.ActivityReplayPolicyRetry,
		}, input, func(ctx context.Context) (workflowCompactionResult, error) {
			if e.options.CompactRound != nil {
				return e.options.CompactRound(ctx, input)
			}

			return workflowCompactionResult{
				Context: input.Execution.Context,
			}, nil
		}, func(result workflowCompactionResult) json.RawMessage {
			return result.Context
		})
}
