package agent

import (
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/harness"
)

func (c *rootWorkflowCallbacks) terminalHooks(
	ctx context.Context,
	input workflowFinishInput,
) (workflowFinishInput, error) {
	if input.Result.Finish.State != models.ExecutionStateCompleted {
		return input, nil
	}

	input.Execution.Context = input.Result.Result.Context

	shared, release, err := c.acquire(ctx, input.Execution, true)
	if err != nil {
		return input, ctxerrors.Wrap(err, "acquire terminal hook callbacks")
	}
	defer release()

	if err := c.refresh(ctx, shared, input.Execution); err != nil {
		return input, ctxerrors.Wrap(err, "restore terminal hook context")
	}

	input.Result.Result = shared.prepared.withWorkflowInboxContinuation(
		input.Result.Result)
	if input.Result.Result.Continue {
		return input, nil
	}

	var conversation workflowConversation
	if err := json.Unmarshal(input.Result.Result.Context,
		&conversation); err != nil {
		return input, ctxerrors.Wrap(err, "decode terminal hook response")
	}

	response := &elelem.Response{FinishReason: conversation.FinishReason}
	if err := shared.prepared.runLifecycleHook(ctx, c.runtime,
		harness.HookEventTurnStop,
		turnStopHookInput(shared.prepared, response)); err != nil {
		return input, ctxerrors.Wrap(err, "run native turn-stop hooks")
	}

	return input, nil
}
