package agent

import (
	"context"
	"encoding/json"

	"github.com/cschleiden/go-workflows/workflow"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const openingHookActivityName = "openingHook"

type workflowOpeningInput struct {
	Execution workflowExecutionInput `json:"execution"`
	Event     harness.HookEvent      `json:"event"`
}

type workflowOpeningResult struct {
	Context        json.RawMessage     `json:"context"`
	Failure        string              `json:"failure,omitempty"`
	RootProjection *rootToolProjection `json:"rootProjection,omitempty"`
}

func (e *workflowExecution) openingHook(ctx context.Context,
	input workflowOpeningInput,
) (workflowActivityResult[workflowOpeningResult], error) {
	if !validOpeningHook(input.Event) {
		return workflowActivityResult[workflowOpeningResult]{},
			ctxerrors.Wrap(commerr.ErrValidationFailed, "opening hook event")
	}

	result, err := runWorkflowCheckpoint(ctx, e, input.Execution,
		session.ScheduleActivityInput{
			StepID: "opening:" + string(input.Event),
			Kind:   models.ActivityKindTool, Name: openingHookActivityName,
			ReplayPolicy: models.ActivityReplayPolicyReconcile,
		}, input, func(ctx context.Context) (workflowOpeningResult, error) {
			if e.options.OpeningHook == nil {
				return workflowOpeningResult{
					Context: input.Execution.Context,
				}, nil
			}

			return e.options.OpeningHook(ctx, input)
		}, func(result workflowOpeningResult) json.RawMessage {
			return result.Context
		})
	if err != nil {
		return result, ctxerrors.Wrap(err, "checkpoint opening hook")
	}

	return e.deliverOpeningHook(ctx, input, result)
}

func (e *workflowExecution) deliverOpeningHook(ctx context.Context,
	input workflowOpeningInput,
	result workflowActivityResult[workflowOpeningResult],
) (workflowActivityResult[workflowOpeningResult], error) {
	if e.options.AfterOpeningHook == nil {
		return result, nil
	}

	return deliverCommittedWorkflowResult(ctx, e, input.Execution, result,
		func(ctx context.Context, output workflowOpeningResult) error {
			return e.options.AfterOpeningHook(ctx, input, output)
		})
}

func validOpeningHook(event harness.HookEvent) bool {
	return event == harness.HookEventPreUserMessage ||
		event == harness.HookEventSessionStart ||
		event == harness.HookEventPostUserMessage ||
		event == harness.HookEventTurnStart
}

func openWorkflow(ctx workflow.Context, input workflowExecutionInput) (
	workflowExecutionInput, error,
) {
	var conversation workflowConversation
	if err := json.Unmarshal(input.Context, &conversation); err != nil {
		return input, ctxerrors.Wrap(err, "decode workflow opening context")
	}

	if conversation.Root == nil {
		return input, nil
	}

	for _, event := range []harness.HookEvent{
		harness.HookEventPreUserMessage, harness.HookEventSessionStart,
		harness.HookEventPostUserMessage, harness.HookEventTurnStart,
	} {
		result, err := awaitWorkflowActivity[workflowOpeningResult](ctx,
			openingHookActivityName, workflowOpeningInput{
				Execution: input,
				Event:     event,
			})
		if err != nil {
			return input, ctxerrors.Wrap(err, "run opening hook checkpoint")
		}

		input.Context = result.Context
		if result.Failure != "" {
			return input, ctxerrors.New(result.Failure)
		}
	}

	return input, nil
}
