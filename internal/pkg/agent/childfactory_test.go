package agent

import (
	"context"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChildFactoryBindsSavedScopeAndDurableInbox(t *testing.T) {
	t.Parallel()
	fixture, input, original := childModelFixture(t,
		elelemtest.Text("restored child reply"))
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	before, err := fixture.base.store.GetAgentRun(t.Context(),
		input.Execution.SessionID, original.run.ID)
	require.NoError(t, err)
	actor, err := fixture.base.runtime.restoreChildWorkflowActor(
		t.Context(), input, cancel, nil)
	require.NoError(t, err)
	assert.Equal(t, original.run.ID, actor.run.ID)
	assert.NotSame(t, original.run, actor.run)
	after, err := fixture.base.store.GetAgentRun(t.Context(),
		input.Execution.SessionID, original.run.ID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	require.NotNil(t, actor.run.inbox)
	require.NoError(t, actor.run.inbox.deliver(t.Context(), "continue child"))
	input.Execution.Context, err = fixture.base.runtime.prepareChildWorkflowRound(
		t.Context(), actor.prepared.scope, input)
	require.NoError(t, err)
	fixture.engine.options.ModelRound = func(
		ctx context.Context, round workflowRoundInput,
	) (workflowRoundResult, error) {
		return fixture.base.runtime.workflowChildModelRound(ctx,
			actor.sink, round)
	}
	result, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, "restored child reply", result.Output.Text)
	reused, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, result.Output, reused.Output)
	count, _ := actor.run.inbox.arrival()
	assert.Zero(t, count)
	actor.run.signalCancel()
	assert.ErrorIs(t, ctx.Err(), context.Canceled)
}

func TestChildFactoryRefusesPausedExecutionAndMissingCancellation(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childModelFixture(t)
	_, err := fixture.base.runtime.restoreChildWorkflowActor(
		t.Context(), input, nil, nil)
	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
	_, err = fixture.base.store.PauseExecution(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID, "keep paused")
	require.NoError(t, err)
	_, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	_, err = fixture.base.runtime.restoreChildWorkflowActor(
		t.Context(), input, cancel, nil)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
}
