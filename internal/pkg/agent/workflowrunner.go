package agent

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/go-workflows/backend"
	"github.com/psyb0t/go-workflows/client"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const (
	workflowPauseTimeout      = 10 * time.Second
	workflowUserStopReason    = session.ExecutionUserStopReason
	workflowResultWaitTimeout = time.Duration(math.MaxInt64)

	// workflowFailurePauseReason pauses a root whose workflow failed, so it
	// stays inspectable and never continues on its own.
	workflowFailurePauseReason = "turn workflow failed"

	reasonTurnWorkflowFailed = "turn_workflow_failed"
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
		} else {
			err = errors.Join(err,
				n.failed(ctx, prepared, submission.Input, err))
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

	// Stop pauses the work rather than cancelling it for good, so the
	// turnCancelled hook does not run. The turn itself still ends, with a
	// stored turn.cancelled frame.
	return n.dispatch.runtime.endUnfinishedTurn(cleanup, prepared,
		session.CancelledTurnEnd())
}

// failed ends a turn whose root workflow failed. The root is paused rather
// than left running, so it stays inspectable and never continues by itself,
// and the session is free for the next message.
func (n *nativeWorkflowRunner) failed(
	ctx context.Context,
	prepared *preparedTurn,
	input workflowExecutionInput,
	cause error,
) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx),
		workflowPauseTimeout)
	defer cancel()

	runtimecontext.Logger(cleanup).Error(
		"turn workflow failed, pausing its root",
		"reason", reasonTurnWorkflowFailed,
		"execution_id", input.ExecutionID,
		"err", cause,
	)

	pauseErr := n.engine.pause(cleanup, input.SessionID, input.ExecutionID,
		workflowFailurePauseReason)
	if pauseErr != nil {
		pauseErr = ctxerrors.Wrap(pauseErr, "pause failed native root")
	}

	return errors.Join(pauseErr, n.dispatch.runtime.endUnfinishedTurn(
		cleanup, prepared, session.FailedTurnEnd()))
}

// endUnfinishedTurn records how a turn ended when its workflow did not finish
// it: the turn's terminal state and frame, and the session's lease, message
// queue, child sink, and turn slot released, so the next message can run.
func (r *Runtime) endUnfinishedTurn(
	ctx context.Context,
	prepared *preparedTurn,
	end session.TurnEnd,
) error {
	_, endErr := r.store.EndRunningTurn(ctx, prepared.lease.SessionID,
		prepared.lease.TurnID, end)
	if endErr != nil {
		endErr = ctxerrors.Wrap(endErr, "end unfinished turn")
	}

	r.store.ReleaseTurn(prepared.lease)
	r.closeActiveUserMessageQueue(prepared)
	prepared.turn.closeChildSink()
	prepared.releaseTurnCapacity()

	return endErr
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
