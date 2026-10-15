package agent

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

type workflowDispatch struct {
	runtime      *Runtime
	root         *rootWorkflowCallbacks
	child        *childWorkflowCallbacks
	rootOptions  workflowExecutionOptions
	childOptions workflowExecutionOptions
}

func newWorkflowDispatch(
	ctx context.Context,
	runtime *Runtime,
	generationID uuid.UUID,
	sink EventSink,
) *workflowDispatch {
	dispatch := &workflowDispatch{
		runtime:      runtime,
		root:         newRootWorkflowCallbacks(runtime, generationID, sink),
		child:        newChildWorkflowCallbacks(ctx, runtime, sink),
		rootOptions:  workflowExecutionOptions{GenerationID: generationID},
		childOptions: workflowExecutionOptions{GenerationID: generationID},
	}
	dispatch.root.bind(&dispatch.rootOptions)
	dispatch.child.bind(&dispatch.childOptions)

	return dispatch
}

func (d *workflowDispatch) bind(options *workflowExecutionOptions) {
	options.OpeningHook = d.openingHook
	options.AfterOpeningHook = d.afterOpeningHook
	options.PrepareRound = d.prepareRound
	options.CompactRound = d.compactRound
	options.CompactionHook = d.compactionHook
	options.ModelRound = d.modelRound
	options.AfterModelRound = d.afterModelRound
	options.ToolCall = d.toolCall
	options.ToolPhase = d.toolPhase
	options.AfterToolPhase = d.afterToolPhase
	options.BeforeFinish = d.beforeFinish
	options.AfterFinish = d.afterFinish
	options.TerminalHooks = d.terminalHooks
	options.AfterPause = d.afterPause
}

func (d *workflowDispatch) afterPause(
	ctx context.Context,
	sessionID uuid.UUID,
	executionIDs []uuid.UUID,
) error {
	store, err := d.notificationStore()
	if err != nil {
		return ctxerrors.Wrap(err, "resolve paused child storage")
	}

	for _, id := range executionIDs {
		execution, err := store.GetExecution(ctx, sessionID, id)
		if err != nil {
			return ctxerrors.Wrap(err, "read paused child execution")
		}

		if execution.Kind != models.ExecutionKindChild ||
			execution.State != models.ExecutionStatePaused {
			continue
		}

		if err := d.child.pause(ctx, execution); err != nil {
			return ctxerrors.Wrap(err, "release paused child capacity")
		}
	}

	return nil
}

func (d *workflowDispatch) callbacks(
	ctx context.Context,
	input workflowExecutionInput,
) (*workflowExecutionOptions, error) {
	store, ok := d.runtime.store.(session.ExecutionStorage)
	if !ok {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"workflow dispatch execution storage")
	}

	execution, err := store.GetExecution(ctx, input.SessionID,
		input.ExecutionID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read dispatched execution")
	}

	switch execution.Kind {
	case models.ExecutionKindRoot:
		return &d.rootOptions, nil
	case models.ExecutionKindChild:
		return &d.childOptions, nil
	}

	return nil, ctxerrors.Wrap(commerr.ErrInvalidState,
		"workflow execution kind has no callbacks")
}

func (d *workflowDispatch) prepareRound(
	ctx context.Context,
	input workflowRoundInput,
) (json.RawMessage, error) {
	callbacks, err := d.callbacks(ctx, input.Execution)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "dispatch round preparation")
	}

	return callbacks.PrepareRound(ctx, input)
}

func (d *workflowDispatch) compactRound(
	ctx context.Context,
	input workflowRoundInput,
) (workflowCompactionResult, error) {
	callbacks, err := d.callbacks(ctx, input.Execution)
	if err != nil {
		return workflowCompactionResult{}, ctxerrors.Wrap(err,
			"dispatch round compaction")
	}

	return callbacks.CompactRound(ctx, input)
}

func (d *workflowDispatch) modelRound(
	ctx context.Context,
	input workflowRoundInput,
) (workflowRoundResult, error) {
	callbacks, err := d.callbacks(ctx, input.Execution)
	if err != nil {
		return workflowRoundResult{}, ctxerrors.Wrap(err,
			"dispatch model round")
	}

	return callbacks.ModelRound(ctx, input)
}

func (d *workflowDispatch) toolCall(
	ctx context.Context,
	input workflowToolInput,
) (workflowToolResult, error) {
	callbacks, err := d.callbacks(ctx, input.Round.Execution)
	if err != nil {
		return workflowToolResult{}, ctxerrors.Wrap(err,
			"dispatch tool call")
	}

	return callbacks.ToolCall(ctx, input)
}

func (d *workflowDispatch) beforeFinish(
	ctx context.Context,
	input workflowFinishInput,
) (workflowFinishResult, error) {
	callbacks, err := d.callbacks(ctx, input.Execution)
	if err != nil {
		return workflowFinishResult{}, ctxerrors.Wrap(err,
			"dispatch final preparation")
	}

	return callbacks.BeforeFinish(ctx, input)
}

func (d *workflowDispatch) toolPhase(
	ctx context.Context,
	input workflowToolPhaseInput,
) (workflowToolPhaseState, error) {
	callbacks, err := d.callbacks(ctx, input.Tool.Round.Execution)
	if err != nil {
		return input.State, ctxerrors.Wrap(err, "dispatch tool phase")
	}

	return callbacks.ToolPhase(ctx, input)
}

func (d *workflowDispatch) afterToolPhase(
	ctx context.Context,
	input workflowToolPhaseInput,
	state workflowToolPhaseState,
) error {
	callbacks, err := d.callbacks(ctx, input.Tool.Round.Execution)
	if err != nil {
		return ctxerrors.Wrap(err, "dispatch committed tool phase")
	}

	if callbacks.AfterToolPhase == nil {
		return nil
	}

	if err := callbacks.AfterToolPhase(ctx, input, state); err != nil {
		return ctxerrors.Wrap(err, "publish committed actor phase")
	}

	if input.Phase == workflowToolPhaseQuestion &&
		state.ChildProjection != nil &&
		state.ChildProjection.Transcript.Question != nil {
		return d.deliverChildQuestion(ctx, input.Tool.Round.Execution,
			state.ChildProjection.Transcript.Question)
	}

	return nil
}

func (d *workflowDispatch) afterFinish(
	ctx context.Context,
	input workflowFinishInput,
	result workflowFinishResult,
) error {
	callbacks, err := d.callbacks(ctx, input.Execution)
	if err != nil {
		return ctxerrors.Wrap(err, "dispatch terminal cleanup")
	}

	if err := callbacks.AfterFinish(ctx, input, result); err != nil {
		return ctxerrors.Wrap(err, "finish dispatched actor cleanup")
	}

	if callbacks == &d.rootOptions {
		if runner := d.runtime.nativeWorkflowRunner(); runner != nil {
			return runner.nudgeAdmittedExecutions(ctx)
		}
	}

	if result.Finish.ChildReport == "" {
		return nil
	}

	return d.notifyChildReport(ctx, input.Execution)
}
