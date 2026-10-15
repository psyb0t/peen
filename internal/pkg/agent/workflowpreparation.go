package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (e *workflowExecution) prepareRound(
	ctx context.Context,
	input workflowRoundInput,
) (workflowActivityResult[json.RawMessage], error) {
	return runWorkflowCheckpoint(ctx, e, input.Execution,
		session.ScheduleActivityInput{
			StepID: fmt.Sprintf("prepare:%d", input.Round),
			Kind:   models.ActivityKindPreparation, Name: prepareActivityName,
			ReplayPolicy: models.ActivityReplayPolicyRetry,
		}, input, func(ctx context.Context) (json.RawMessage, error) {
			return e.options.PrepareRound(ctx, input)
		}, func(prepared json.RawMessage) json.RawMessage {
			return prepared
		})
}
