package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChildCallbackAdmissionAllowsReadersAndCancelsWaitingWriter(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childModelFixture(t)
	callbacks := newChildWorkflowCallbacks(t.Context(), fixture.base.runtime, nil)
	shared, firstCtx, first, err := callbacks.acquire(t.Context(), input, false)
	require.NoError(t, err)
	t.Cleanup(first)
	_, _, second, err := callbacks.acquire(t.Context(), input, false)
	require.NoError(t, err)
	t.Cleanup(second)
	ctx, cancel := context.WithTimeout(t.Context(), rootCallbackGateTestTimeout)
	t.Cleanup(cancel)
	refused, refusedCtx, refusedRelease, err := callbacks.acquire(
		ctx, input, true)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Nil(t, refused)
	assert.Nil(t, refusedCtx)
	assert.Nil(t, refusedRelease)
	registry, err := fixture.base.runtime.sessionAgentRuns(
		input.Execution.SessionID)
	require.NoError(t, err)
	registry.Cancel(shared.actor.run.ID)
	select {
	case <-firstCtx.Done():
	case <-t.Context().Done():
		t.Fatal("child callback cancellation did not reach admission")
	}
	require.ErrorIs(t, firstCtx.Err(), context.Canceled)
	refused, refusedCtx, refusedRelease, err = callbacks.acquire(
		t.Context(), input, true)
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, refused)
	assert.Nil(t, refusedCtx)
	assert.Nil(t, refusedRelease)
}

func TestChildCallbackAdmissionRefusesPausedExecution(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childModelFixture(t)
	callbacks := newChildWorkflowCallbacks(t.Context(), fixture.base.runtime, nil)
	shared, err := callbacks.resolve(t.Context(), input)
	require.NoError(t, err)
	reader, err := shared.gate.acquire(t.Context(), false)
	require.NoError(t, err)
	_, err = fixture.base.store.PauseExecution(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID, "pause admission")
	require.NoError(t, err)
	reader()
	refused, refusedCtx, refusedRelease, err := callbacks.acquire(
		t.Context(), input, true)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	assert.Nil(t, refused)
	assert.Nil(t, refusedCtx)
	assert.Nil(t, refusedRelease)
}

func TestChildCallbackRefreshKeepsInboxAndRestoresPendingCounts(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childModelFixture(t)
	callbacks := newChildWorkflowCallbacks(t.Context(), fixture.base.runtime, nil)
	shared, ctx, release, err := callbacks.acquire(t.Context(), input, true)
	require.NoError(t, err)
	t.Cleanup(release)
	inbox := shared.actor.run.inbox
	require.NoError(t, inbox.deliver(t.Context(), "retained arrival"))
	_, arrived := inbox.arrival()
	require.NoError(t, callbacks.refresh(ctx, shared, input))
	assert.Same(t, inbox, shared.actor.run.inbox)
	count, after := inbox.arrival()
	assert.Equal(t, 1, count)
	assert.Equal(t, arrived, after)
}

func TestChildCallbackResumeCancelsAndDrainsBeforeReplacement(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childModelFixture(t)
	callbacks := newChildWorkflowCallbacks(t.Context(), fixture.base.runtime, nil)
	before, active, release, err := callbacks.acquire(t.Context(), input, false)
	require.NoError(t, err)
	t.Cleanup(release)
	_, err = fixture.base.store.PauseExecution(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID, "pause active child")
	require.NoError(t, err)
	_, err = fixture.base.store.ResumeExecution(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID)
	require.NoError(t, err)
	finished := make(chan *childCallbackScope, 1)
	errors := make(chan error, 1)
	var group sync.WaitGroup
	group.Go(func() {
		after, resolveErr := callbacks.resolve(t.Context(), input)
		errors <- resolveErr
		finished <- after
	})
	select {
	case <-active.Done():
	case <-t.Context().Done():
		t.Fatal("resume did not cancel old admission")
	}
	registry, err := fixture.base.runtime.sessionAgentRuns(
		input.Execution.SessionID)
	require.NoError(t, err)
	installed, found := registry.Get(before.actor.run.ID)
	require.True(t, found)
	assert.Same(t, before.actor.run, installed)
	release()
	group.Wait()
	require.NoError(t, <-errors)
	after := <-finished
	require.NotNil(t, after)
	assert.NotSame(t, before, after)
	require.ErrorIs(t, callbacks.validateAdmission(t.Context(), before, input),
		commerr.ErrConflict)
	after.actor.run.inbox.close()
	require.ErrorIs(t, callbacks.refresh(t.Context(), after, input),
		commerr.ErrInvalidState)
}
