package agent

import (
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/elelem"
)

func executeWorkflowToolEffect(
	ctx context.Context,
	tool elelem.Tool,
	event *elelem.ToolEvent,
	arguments json.RawMessage,
	phase workflowToolPhase,
	state *workflowToolPhaseState,
) error {
	switch phase {
	case workflowToolPhasePre:
		return workflowToolHook(ctx, event, elelem.ToolPhasePreRun, tool.PreRun)
	case workflowToolPhaseHandler:
		if tool.Handler == nil {
			result := elelem.NewToolErrorResult("tool has no handler")
			event.Result, state.Finished = &result, true

			return nil
		}

		return runWorkflowToolHandler(ctx, tool, event, arguments)
	case workflowToolPhaseOutcome:
		return executeWorkflowOutcomeHook(ctx, tool, event, state)
	case workflowToolPhasePost:
		return executeWorkflowHookAndInjection(ctx, event,
			elelem.ToolPhasePostRun, tool.PostRun,
			tool.PostRunMessageInjector, state)
	case workflowToolPhaseProject, workflowToolPhaseQuestion:
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"tool projection cannot execute effects")
	}

	return ctxerrors.Wrap(commerr.ErrValidationFailed, "unknown tool phase")
}

func executeWorkflowOutcomeHook(
	ctx context.Context,
	tool elelem.Tool,
	event *elelem.ToolEvent,
	state *workflowToolPhaseState,
) error {
	phase, hook, injector := elelem.ToolPhaseOnSuccess,
		tool.OnSuccess, tool.OnSuccessMessageInjector
	if event.Err != nil || event.Result.IsError {
		phase, hook, injector = elelem.ToolPhaseOnError,
			tool.OnError, tool.OnErrorMessageInjector
	}

	return executeWorkflowHookAndInjection(ctx, event, phase, hook,
		injector, state)
}

func executeWorkflowHookAndInjection(
	ctx context.Context,
	event *elelem.ToolEvent,
	phase elelem.ToolPhase,
	hook elelem.ToolHook,
	injector elelem.MessageInjector,
	state *workflowToolPhaseState,
) error {
	if err := workflowToolHook(ctx, event, phase, hook); err != nil {
		return ctxerrors.Wrap(err, "execute phase hook")
	}

	if err := ctx.Err(); err != nil {
		return ctxerrors.Wrap(err, "tool phase interrupted before injection")
	}

	if injector == nil {
		return nil
	}

	event.Phase = phase

	injection, err := injector(ctx, event)
	if err != nil {
		return ctxerrors.Wrap(err, "execute phase injection")
	}

	if injection == nil {
		return nil
	}

	if !validWorkflowInjectionRole(injection.Type) {
		ctxscope.GetLogger(ctx).Error("tool injection has an unusable role",
			"reason", elelem.LogReasonInjectionRoleInvalid,
			"tool", event.Tool.Name)

		return nil
	}

	copied := *injection
	copied.Phase, copied.Tool = phase, event.Tool.Name
	copied.CallID, copied.Round = event.CallID, event.Round
	state.Outcome.Injections = append(state.Outcome.Injections, copied)

	return nil
}
