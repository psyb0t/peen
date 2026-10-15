package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/hooks"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (c *rootWorkflowCallbacks) openingHook(ctx context.Context,
	input workflowOpeningInput,
) (workflowOpeningResult, error) {
	shared, release, err := c.acquire(ctx, input.Execution, true)
	if err != nil {
		return workflowOpeningResult{}, ctxerrors.Wrap(err,
			"admit root opening hook")
	}
	defer release()

	prepared, scope, err := c.openingScope(ctx, shared, input)
	if err != nil {
		return workflowOpeningResult{}, err
	}

	result := workflowOpeningResult{Context: input.Execution.Context}
	if err := c.runtime.runOpeningHook(ctx, prepared, input.Event); err != nil {
		if ctx.Err() != nil || !errors.Is(err, hooks.ErrDenied) {
			return result, ctxerrors.Wrap(err, "run root opening hook")
		}

		result.Failure = toolErrorMessage(err)
	}

	current := scope.context()
	current.prompt = prepared.prompt.SystemMessage()
	current.promptHash = prepared.promptHash
	current.contextHash = prepared.contextHash

	conversation, err := c.runtime.rootWorkflowConversation(prepared, scope)
	if err != nil {
		return result, ctxerrors.Wrap(err, "snapshot opening hook context")
	}

	result.Context, err = json.Marshal(conversation)
	if err != nil {
		return result, ctxerrors.Wrap(err, "encode opening hook context")
	}

	lease, err := rootCompactionLease(ctx)
	if err != nil {
		return result, ctxerrors.Wrap(err, "read opening hook lease")
	}

	if lease.GenerationID != c.generationID {
		return result, ctxerrors.Wrap(commerr.ErrConflict,
			"opening hook generation changed")
	}

	result.RootProjection = prepared.rootToolProjection(shared, lease.ID)

	return result, nil
}

func (c *rootWorkflowCallbacks) openingScope(ctx context.Context,
	shared *rootCallbackScope, input workflowOpeningInput,
) (*preparedTurn, *directoryScope, error) {
	var conversation workflowConversation

	err := json.Unmarshal(input.Execution.Context, &conversation)
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "decode opening root scope")
	}

	seed := *shared.prepared
	seed.workflowInbox = false
	seed.turn = newRootPhaseTurn(shared.prepared.turn)
	seed.attachPublisher(ctx)

	prepared, scope, err := c.runtime.restoreRootWorkflowScope(ctx, &seed,
		conversation)
	if err != nil {
		return nil, nil, ctxerrors.Wrap(err, "restore opening root scope")
	}

	prepared.workflowInbox = shared.prepared.workflowInbox

	return prepared, scope, nil
}

func (r *Runtime) runOpeningHook(ctx context.Context, prepared *preparedTurn,
	event harness.HookEvent,
) error {
	//nolint:exhaustive // Only the four validated opening events dispatch here.
	switch event {
	case harness.HookEventPreUserMessage:
		prompt, err := r.appendPreUserHookContext(ctx, prepared.snapshot,
			prepared.workspace, prepared.input, prepared.prompt.SystemMessage())
		if err != nil {
			return ctxerrors.Wrap(err, "run saved pre-user-message hooks")
		}

		prepared.prompt = prepared.prompt.WithSystem(prompt)

		prepared.contextHash, prepared.promptHash, err = r.saveSnapshots(ctx,
			prepared.snapshot, prompt, prompt)
		if err != nil {
			return ctxerrors.Wrap(err, "save opening prompt snapshots")
		}

		return nil
	case harness.HookEventSessionStart:
		if !prepared.opened.Created {
			return nil
		}

		return prepared.runLifecycleHook(ctx, r, event, sessionStartHookInput())
	case harness.HookEventPostUserMessage:
		return prepared.runLifecycleHook(ctx, r, event,
			userMessageHookInput(prepared.input, prepared.workspace))
	case harness.HookEventTurnStart:
		return r.startLease(ctx, prepared)
	default:
		return ctxerrors.Wrap(commerr.ErrValidationFailed, "root opening event")
	}
}

func (c *rootWorkflowCallbacks) afterOpeningHook(ctx context.Context,
	input workflowOpeningInput, result workflowOpeningResult,
) error {
	if result.RootProjection == nil {
		return nil
	}

	shared, release, err := c.acquire(ctx, input.Execution, true)
	if err != nil {
		return ctxerrors.Wrap(err, "admit committed opening hook")
	}
	defer release()

	store, ok := c.runtime.store.(session.ExecutionStorage)
	if !ok {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"opening projection storage")
	}

	activity, err := store.GetExecutionActivity(ctx, input.Execution.SessionID,
		result.RootProjection.ActivityID)
	if err != nil {
		return ctxerrors.Wrap(err, "read committed opening projection")
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		return ctxerrors.Wrap(err, "encode opening projection")
	}

	encodedInput, err := json.Marshal(input)
	if err != nil {
		return ctxerrors.Wrap(err, "encode opening projection input")
	}

	if !matchesOpeningProjection(activity, input,
		string(encodedInput), string(encoded)) {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"opening projection differs from committed checkpoint")
	}

	turn := shared.prepared.turn

	release()

	return shared.deliverRootPhaseProjection(ctx, turn, result.RootProjection)
}

func matchesOpeningProjection(activity *models.ExecutionActivity,
	input workflowOpeningInput, encodedInput, encodedOutput string,
) bool {
	return activity.ExecutionID == input.Execution.ExecutionID &&
		activity.State == models.ActivityStateCompleted &&
		activity.Name == openingHookActivityName &&
		activity.OutputJSON != nil &&
		*activity.OutputJSON == encodedOutput &&
		activity.InputJSON == encodedInput
}

func (d *workflowDispatch) openingHook(ctx context.Context,
	input workflowOpeningInput,
) (workflowOpeningResult, error) {
	callbacks, err := d.callbacks(ctx, input.Execution)
	if err != nil {
		return workflowOpeningResult{}, ctxerrors.Wrap(err,
			"dispatch opening hook")
	}

	if callbacks.OpeningHook == nil {
		return workflowOpeningResult{Context: input.Execution.Context}, nil
	}

	return callbacks.OpeningHook(ctx, input)
}

func (d *workflowDispatch) afterOpeningHook(ctx context.Context,
	input workflowOpeningInput, result workflowOpeningResult,
) error {
	callbacks, err := d.callbacks(ctx, input.Execution)
	if err != nil {
		return ctxerrors.Wrap(err, "dispatch committed opening hook")
	}

	if callbacks.AfterOpeningHook == nil {
		return nil
	}

	return callbacks.AfterOpeningHook(ctx, input, result)
}
