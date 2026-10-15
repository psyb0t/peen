package agent

import (
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (c *childWorkflowCallbacks) acquire(
	ctx context.Context,
	input workflowRoundInput,
	exclusive bool,
) (*childCallbackScope, context.Context, func(), error) {
	shared, err := c.resolve(ctx, input)
	if err != nil {
		return nil, nil, nil, ctxerrors.Wrap(err, "resolve admitted child")
	}

	admitted, cancel := context.WithCancel(ctx)
	admitted = contextWithParentAgentRun(admitted, shared.actor.run)
	admitted = contextWithAgentDepth(admitted, shared.actor.run.Depth)

	stop := context.AfterFunc(shared.context, cancel)
	if shared.context.Err() != nil {
		cancel()
	}

	release, err := shared.gate.acquire(admitted, exclusive)
	if err != nil {
		stop()
		cancel()

		return nil, nil, nil, ctxerrors.Wrap(err, "admit child callback scope")
	}

	cleanup := func() {
		release()
		stop()
		cancel()
	}
	if err := c.validateAdmission(admitted, shared, input); err != nil {
		cleanup()

		return nil, nil, nil, ctxerrors.Wrap(err, "validate admitted child")
	}

	return shared, admitted, cleanup, nil
}

func (c *childWorkflowCallbacks) validateAdmission(
	ctx context.Context,
	shared *childCallbackScope,
	input workflowRoundInput,
) error {
	revision, err := c.readRevision(ctx, input)
	if err != nil {
		return ctxerrors.Wrap(err, "recheck admitted child revision")
	}

	if revision != shared.revision {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"admitted child control revision changed")
	}

	registry, err := c.runtime.sessionAgentRuns(input.Execution.SessionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read admitted child registry")
	}

	current, found := registry.Get(shared.actor.run.ID)
	if !found || current != shared.actor.run {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"admitted child registry owner changed")
	}

	return nil
}

func drainChildCallbackScope(
	ctx context.Context,
	previous *childCallbackScope,
) (func(), error) {
	if previous == nil {
		return func() {}, nil
	}

	previous.cancel()

	release, err := previous.gate.acquire(ctx, true)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "wait for previous child callbacks")
	}

	return release, nil
}

func (c *childWorkflowCallbacks) refresh(
	ctx context.Context,
	shared *childCallbackScope,
	input workflowRoundInput,
) error {
	var conversation workflowConversation
	if err := json.Unmarshal(input.Execution.Context,
		&conversation); err != nil {
		return ctxerrors.Wrap(err, "decode refreshed child context")
	}

	deps, prepared, err := c.runtime.restoreChildWorkflowScope(ctx,
		shared.actor.deps, shared.actor.run, conversation)
	if err != nil {
		return ctxerrors.Wrap(err, "restore refreshed child scope")
	}

	store, ok := c.runtime.store.(session.ExecutionStorage)
	if !ok {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"refreshed child inbox storage")
	}

	if err := refreshChildCallbackInbox(ctx, shared.actor.run.inbox, store,
		deps.executor.Workspace()); err != nil {
		return ctxerrors.Wrap(err, "refresh child callback inbox")
	}

	shared.actor.deps, shared.actor.prepared = deps, prepared

	return nil
}

func refreshChildCallbackInbox(
	ctx context.Context,
	inbox *agentInbox,
	store session.ExecutionStorage,
	directory string,
) error {
	inbox.mutex.Lock()
	defer inbox.mutex.Unlock()

	native := inbox.workflow
	native.mutex.Lock()
	defer native.mutex.Unlock()

	if inbox.closed || native.closed {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"refreshed child inbox is closed")
	}

	pending, err := readWorkflowChildPending(ctx, store, native.sessionID,
		native.executionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read refreshed child pending identities")
	}

	native.pending, native.directory = pending, directory

	return nil
}
