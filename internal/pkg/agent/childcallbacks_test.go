package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChildCallbacksShareOneBoundActor(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childModelFixture(t)
	callbacks := newChildWorkflowCallbacks(t.Context(),
		fixture.base.runtime, nil)
	first, err := callbacks.resolve(t.Context(), input)
	require.NoError(t, err)
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			shared, resolveErr := callbacks.resolve(t.Context(), input)
			assert.NoError(t, resolveErr)
			assert.Same(t, first, shared)
		})
	}
	group.Wait()
	registry, err := fixture.base.runtime.sessionAgentRuns(
		input.Execution.SessionID)
	require.NoError(t, err)
	runs := registry.List()
	require.Len(t, runs, 1)
	assert.Same(t, first.actor.run, runs[0])
	_, found := registry.Cancel(first.actor.run.ID)
	assert.True(t, found)
	assert.ErrorIs(t, first.context.Err(), context.Canceled)
}

func TestChildCallbacksResumeReplacesActorAndRetainsQueuedMessages(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childModelFixture(t)
	callbacks := newChildWorkflowCallbacks(t.Context(),
		fixture.base.runtime, nil)
	before, err := callbacks.resolve(t.Context(), input)
	require.NoError(t, err)
	require.NoError(t, before.actor.run.inbox.deliver(t.Context(),
		"saved message"))
	_, err = fixture.base.store.PauseExecution(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID, "paused")
	require.NoError(t, err)
	_, err = callbacks.resolve(t.Context(), input)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	_, err = fixture.base.store.ResumeExecution(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID)
	require.NoError(t, err)
	after, err := callbacks.resolve(t.Context(), input)
	require.NoError(t, err)
	assert.NotSame(t, before, after)
	assert.Equal(t, before.actor.run.ID, after.actor.run.ID)
	assert.ErrorIs(t, before.context.Err(), context.Canceled)
	require.ErrorIs(t, before.actor.run.inbox.deliver(t.Context(), "old callback"),
		session.ErrAgentRunFinished)
	count, _ := after.actor.run.inbox.arrival()
	assert.Equal(t, 1, count)
	require.NoError(t, after.context.Err())
}

func TestChildCallbacksRefuseAnotherRegistryOwner(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childModelFixture(t)
	first := newChildWorkflowCallbacks(t.Context(), fixture.base.runtime, nil)
	shared, err := first.resolve(t.Context(), input)
	require.NoError(t, err)
	other := newChildWorkflowCallbacks(t.Context(), fixture.base.runtime, nil)
	_, err = other.resolve(t.Context(), input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	registry, err := fixture.base.runtime.sessionAgentRuns(
		input.Execution.SessionID)
	require.NoError(t, err)
	current, found := registry.Get(shared.actor.run.ID)
	require.True(t, found)
	assert.Same(t, shared.actor.run, current)
	require.NoError(t, shared.context.Err())
}

func TestChildCallbacksCancelledConstructionAdmission(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childModelFixture(t)
	callbacks := newChildWorkflowCallbacks(t.Context(), fixture.base.runtime, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := callbacks.resolve(ctx, input)
	require.ErrorIs(t, err, context.Canceled)
	registry, err := fixture.base.runtime.sessionAgentRuns(
		input.Execution.SessionID)
	require.NoError(t, err)
	assert.Empty(t, registry.List())
}

func TestChildCallbacksKeepLiveSinkForModelEvents(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childModelFixture(t, elelemtest.Text("live child"))
	var received []Event
	callbacks := newChildWorkflowCallbacks(t.Context(), fixture.base.runtime,
		func(event Event) error {
			received = append(received, event)

			return nil
		})
	shared, err := callbacks.resolve(t.Context(), input)
	require.NoError(t, err)
	fixture.engine.options.ModelRound = func(
		ctx context.Context, round workflowRoundInput,
	) (workflowRoundResult, error) {
		return fixture.base.runtime.workflowChildModelRound(ctx,
			shared.actor.sink, round)
	}
	_, err = fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	assert.NotEmpty(t, received)
}
