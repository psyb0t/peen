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

type workflowFinishInput struct {
	Execution workflowExecutionInput `json:"execution"`
	Result    workflowFinishResult   `json:"result"`
	Round     int                    `json:"round,omitempty"`
}

type workflowFinishResult struct {
	Result   workflowRoundResult          `json:"result"`
	Finish   session.FinishExecutionInput `json:"finish"`
	Continue bool                         `json:"continue,omitempty"`

	AllowContinuation bool `json:"allowContinuation,omitempty"`
}

func (e *workflowExecution) finishExecution(
	ctx context.Context,
	input workflowFinishInput,
) (workflowActivityResult[workflowFinishResult], error) {
	if input.Round < 0 {
		return workflowActivityResult[workflowFinishResult]{},
			ctxerrors.Wrap(commerr.ErrValidationFailed, "final decision round")
	}

	step := session.ExecutionFinishStepID
	if input.Round > 0 {
		step = fmt.Sprintf("%s:%d", session.ExecutionFinishStepID, input.Round)
	}

	result, err := runWorkflowCheckpoint(ctx, e, input.Execution,
		session.ScheduleActivityInput{
			StepID:       step,
			Kind:         models.ActivityKindPreparation,
			Name:         session.ExecutionFinishActivityName,
			ReplayPolicy: models.ActivityReplayPolicyRetry,
		}, input, func(ctx context.Context) (workflowFinishResult, error) {
			if e.options.BeforeFinish != nil {
				return e.options.BeforeFinish(ctx, input)
			}

			return input.Result, nil
		}, func(result workflowFinishResult) json.RawMessage {
			return result.Result.Context
		})
	if err != nil {
		return result, ctxerrors.Wrap(err, "record final workflow decision")
	}

	if result.Decision != session.ActivityDecisionExecute &&
		result.Decision != session.ActivityDecisionReuse {
		return result, nil
	}

	if result.Output.Continue || e.options.AfterFinish == nil {
		return result, nil
	}

	_, err = invokeWorkflowPhase(ctx, func(ctx context.Context) (
		struct{}, error,
	) {
		return struct{}{}, e.options.AfterFinish(ctx, input, result.Output)
	})
	if err != nil {
		return result, ctxerrors.Wrap(err, "retire finished workflow callbacks")
	}

	return result, nil
}
