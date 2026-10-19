package agent

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/cschleiden/go-workflows/backend"
	"github.com/cschleiden/go-workflows/client"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const (
	workflowPauseTimeout      = 10 * time.Second
	workflowUserStopReason    = session.ExecutionUserStopReason
	workflowResultWaitTimeout = time.Duration(math.MaxInt64)
)

type nativeWorkflowRunner struct {
	api      *client.Client
	dispatch *workflowDispatch
	engine   *workflowExecution
}

func (r *Runtime) bindWorkflowRunner(
	storage backend.Backend,
	dispatch *workflowDispatch,
	engine *workflowExecution,
	sessionID uuid.UUID,
) error {
	r.workflowMutex.Lock()
	defer r.workflowMutex.Unlock()

	if sessionID != r.sessionID {
		return ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"native runner session differs")
	}

	if r.workflowRunner != nil {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"native runner is already bound")
	}

	r.workflowRunner = &nativeWorkflowRunner{
		api: newWorkflowClient(storage), dispatch: dispatch, engine: engine,
	}

	return nil
}

func (r *Runtime) nativeWorkflowRunner() *nativeWorkflowRunner {
	r.workflowMutex.RLock()
	defer r.workflowMutex.RUnlock()

	return r.workflowRunner
}

func (n *nativeWorkflowRunner) run(
	ctx context.Context,
	prepared *preparedTurn,
	admitted func(),
) (*TurnResult, error) {
	runtime := n.dispatch.runtime

	ctx, cancel := context.WithCancel(prepared.scopedContext(ctx))
	defer cancel()

	if err := runtime.registerActiveUserMessageQueue(prepared); err != nil {
		return nil, n.failOpening(ctx, prepared, err)
	}

	runtime.store.RegisterCancellation(prepared.lease, cancel)
	prepared.attachPublisher(context.WithoutCancel(ctx))

	if err := prepared.turn.publishInitialEvents(); err != nil {
		return nil, n.failOpening(ctx, prepared, err)
	}

	submission, err := n.dispatch.submitRoot(ctx, n.api, prepared)
	if err != nil {
		if submission.Input.ExecutionID == uuid.Nil {
			return nil, n.failOpening(ctx, prepared, err)
		}

		return nil, ctxerrors.Wrap(errors.Join(err,
			n.cleanupFailedSubmission(ctx, prepared, submission.Input)),
			"submit admitted native root")
	}

	admitted()

	result, err := client.GetWorkflowResult[workflowRoundResult](ctx, n.api,
		submission.Instance, workflowResultWaitTimeout)
	if err != nil {
		if ctx.Err() != nil {
			err = errors.Join(err,
				n.interrupted(ctx, prepared, submission.Input))
		}

		return nil, ctxerrors.Wrap(err, "wait for native root result")
	}

	return nativeRootTurnResult(prepared, result)
}

func (n *nativeWorkflowRunner) failOpening(
	ctx context.Context,
	prepared *preparedTurn,
	cause error,
) error {
	defer prepared.releaseTurnCapacity()

	runtime := n.dispatch.runtime
	defer runtime.closeActiveUserMessageQueue(prepared)

	prepared.turn.closeChildSink()

	return runtime.finalizeFailedTurn(ctx, prepared, cause)
}

func (n *nativeWorkflowRunner) interrupted(
	ctx context.Context,
	prepared *preparedTurn,
	input workflowExecutionInput,
) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx),
		workflowPauseTimeout)
	defer cancel()

	store := n.engine.options.Store

	turn, err := store.GetExecutionRootTurn(cleanup, input.SessionID,
		input.ExecutionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read interrupted native turn")
	}

	if !turn.CancelRequested {
		return nil
	}

	if turn.State == models.TurnStateCompleted ||
		turn.State == models.TurnStateFailed {
		return nil
	}

	if err := n.engine.pause(cleanup, input.SessionID, input.ExecutionID,
		workflowUserStopReason); err != nil {
		return ctxerrors.Wrap(err, "pause explicitly stopped native root")
	}

	n.dispatch.runtime.store.ReleaseTurn(prepared.lease)
	n.dispatch.runtime.closeActiveUserMessageQueue(prepared)

	return nil
}

func nativeRootTurnResult(
	prepared *preparedTurn,
	result workflowRoundResult,
) (*TurnResult, error) {
	var conversation workflowConversation
	if err := json.Unmarshal(result.Context, &conversation); err != nil {
		return nil, ctxerrors.Wrap(err, "decode native root result context")
	}

	prepared.turn.mutex.Lock()
	events := append([]Event(nil), prepared.turn.events...)
	prepared.turn.mutex.Unlock()

	return &TurnResult{
		SessionID: prepared.lease.SessionID, Created: prepared.opened.Created,
		Text: result.Text, Thinking: conversation.reasoning(),
		Model: conversation.ModelReference, Events: events,
		FinishReason: conversation.FinishReason,
		HasToolCalls: conversation.HasToolCalls,
		OutputTokens: conversation.Usage.Completion,
	}, nil
}
