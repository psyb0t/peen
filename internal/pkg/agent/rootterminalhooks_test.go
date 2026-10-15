package agent

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/hooks"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootTerminalHooksPreserveNativeTurnStop(t *testing.T) {
	t.Parallel()
	base := newNativeRunnerFixture(t,
		elelemtest.NewScriptedDriver(elelemtest.Text("terminal answer")))
	var calls atomic.Int64
	invoked := make(chan hooks.Invocation, 1)
	base.runtime.hookCallback = func(
		_ context.Context, input hooks.Invocation,
	) (
		hooks.Outcome, error,
	) {
		if input.Event == harness.HookEventTurnStop {
			calls.Add(1)
			invoked <- input
		}

		return hooks.Outcome{}, nil
	}
	requestID := uuid.New()
	result, err := base.runtime.Run(t.Context(), TurnRequest{
		RequestID: requestID, Message: "finish native work",
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), calls.Load())
	input := <-invoked
	assert.Equal(t, requestID, input.RequestID)
	assert.Equal(t, result.SessionID, input.SessionID)
	var payload turnStopHookPayload
	require.NoError(t, json.Unmarshal(input.Input, &payload))
	assert.Equal(t, string(elelem.FinishReasonStop), payload.FinishReason)
	assert.Equal(t, base.workspace, payload.Workspace)
	assertRootTerminalHookRecord(t, base, result.SessionID)
}

func TestRootTerminalHooksSkipQueuedContinuation(t *testing.T) {
	t.Parallel()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	var calls atomic.Int64
	fixture.base.runtime.hookCallback = func(
		context.Context, hooks.Invocation,
	) (
		hooks.Outcome, error,
	) {
		calls.Add(1)

		return hooks.Outcome{}, nil
	}
	require.NoError(t, prepared.userMessages.enqueue(t.Context(), TurnRequest{
		RequestID: uuid.New(), Message: "continue with queued input",
	}))
	dispatch := newWorkflowDispatch(t.Context(), fixture.base.runtime,
		fixture.engine.options.GenerationID, nil)
	result, err := dispatch.terminalHooks(t.Context(),
		callbackFinishInput(fixture))
	require.NoError(t, err)
	assert.True(t, result.Result.Result.Continue)
	assert.Zero(t, calls.Load())
}

func TestRootTerminalHooksDoNotRunParentHookForChild(t *testing.T) {
	t.Parallel()
	fixture, child, _ := childModelFixture(t)
	dispatch := newWorkflowDispatch(t.Context(), fixture.base.runtime,
		fixture.engine.options.GenerationID, nil)
	input := workflowFinishInput{Execution: child.Execution}
	result, err := dispatch.terminalHooks(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, input, result)
}

func TestRootTerminalHooksSkipFailedTurnStop(t *testing.T) {
	t.Parallel()
	callbacks := newRootWorkflowCallbacks(&Runtime{}, uuid.New(), nil)
	input := workflowFinishInput{Result: workflowFinishResult{
		Finish: session.FinishExecutionInput{
			State: models.ExecutionStateFailed,
		},
	}}
	result, err := callbacks.terminalHooks(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, input, result)
}

func assertRootTerminalHookRecord(
	t *testing.T,
	base runtimeFixture,
	sessionID uuid.UUID,
) {
	t.Helper()
	page, err := base.store.SearchExecutions(t.Context(), sessionID,
		session.SearchExecutionsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, agentWorkflowName, page.Items[0].WorkflowVersion)
	activities, err := base.store.ListExecutionActivities(t.Context(), sessionID,
		page.Items[0].ID, session.ListEventsOptions{})
	require.NoError(t, err)
	count := 0
	for _, activity := range activities.Items {
		if activity.Name != terminalHooksActivityName {
			continue
		}

		count++
		assert.Equal(t, models.ActivityStateCompleted, activity.State)
		assert.Equal(t, models.ActivityReplayPolicyReconcile,
			activity.ReplayPolicy)
	}
	assert.Equal(t, 1, count)
}
