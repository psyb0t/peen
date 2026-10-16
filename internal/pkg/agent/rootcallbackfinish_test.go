package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cschleiden/go-workflows/client"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootCallbackFinalEventSurvivesRegisteredRetry(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		turn     elelemtest.Turn
		state    models.TurnState
		kind     string
		isFailed bool
	}{
		{
			name: "completed", turn: elelemtest.Text("finished"),
			state: models.TurnStateCompleted, kind: EventTypeTurnCompleted,
		},
		{
			name: "failed", turn: elelemtest.ToolCall(
				runtimeToolCallID, toolNameReadFile, `{}`),
			state: models.TurnStateFailed, kind: EventTypeTurnFailed,
			isFailed: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			driver := elelemtest.NewScriptedDriver(tc.turn)
			fixture, callbacks, shared := newRootCallbackFixture(t, driver)
			fixture.input.MaxRounds = 1
			var attempts atomic.Int32
			fixture.engine.options.AfterFinish = func(ctx context.Context,
				input workflowFinishInput, result workflowFinishResult,
			) error {
				if attempts.Add(1) == 1 {
					return ctxerrors.Wrap(commerr.ErrConflict,
						"example post-commit interruption")
				}

				return callbacks.afterFinish(ctx, input, result)
			}
			instance := fixture.start(t)
			_, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
				fixture.api, instance, workflowTestResultTimeout)
			require.Equal(t, tc.isFailed, err != nil)
			turn, err := fixture.base.store.GetExecutionRootTurn(t.Context(),
				fixture.input.SessionID, fixture.input.ExecutionID)
			require.NoError(t, err)
			assert.Equal(t, tc.state, turn.State)
			assert.EqualValues(t, 2, attempts.Load())
			assert.Equal(t, 1, driver.Calls())
			assertRootFinalEvent(t, fixture, tc.kind)
			assertCallbackQueueClosed(t, shared, true)
			fixture.stop()
		})
	}
}

func callbackFinishInput(fixture executionLoopFixture) workflowFinishInput {
	return workflowFinishInput{
		Execution: fixture.input,
		Result: workflowFinishResult{
			Result: workflowRoundResult{Context: fixture.input.Context},
			Finish: session.FinishExecutionInput{
				State: models.ExecutionStateCompleted,
			},
		},
	}
}

func assertCallbackQueueClosed(
	t *testing.T,
	shared *rootCallbackScope,
	want bool,
) {
	t.Helper()
	queue := shared.prepared.userMessages
	queue.mutex.Lock()
	defer queue.mutex.Unlock()
	assert.Equal(t, want, queue.closed)
}

func assertRootFinalEvent(
	t *testing.T,
	fixture executionLoopFixture,
	kind string,
) {
	t.Helper()
	execution, err := fixture.base.store.GetExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	page, err := fixture.base.store.ListEvents(t.Context(),
		fixture.input.SessionID, session.ListEventsOptions{})
	require.NoError(t, err)
	count := 0
	for _, event := range page.Items {
		if event.EventType != EventTypeTurnCompleted &&
			event.EventType != EventTypeTurnFailed {
			continue
		}
		count++
		assert.Equal(t, kind, event.EventType)
		assert.Equal(t, execution.RootTurnID, event.TurnID)
		assert.NotEqual(t, uuid.Nil, event.RequestID)
		assert.Equal(t, fixture.engine.options.GenerationID.String(),
			event.WorkerGenerationID)
	}
	assert.Equal(t, 1, count)
}

func TestRootCallbackFinishRetiresOnlyTerminalScopes(t *testing.T) {
	t.Parallel()
	for _, state := range []models.ExecutionState{
		models.ExecutionStateCompleted, models.ExecutionStateFailed,
	} {
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			fixture, callbacks, shared := newRootCallbackFixture(t,
				elelemtest.NewScriptedDriver())
			input := callbackFinishInput(fixture)
			input.Result.Finish.State = state
			if state == models.ExecutionStateFailed {
				input.Result.Finish.FailureDetail = "example failure"
			}

			result, err := fixture.engine.finishExecution(t.Context(), input)
			require.NoError(t, err)
			turn, err := fixture.base.store.GetExecutionRootTurn(t.Context(),
				fixture.input.SessionID, fixture.input.ExecutionID)
			require.NoError(t, err)
			want := models.TurnStateCompleted
			kind := EventTypeTurnCompleted
			if state == models.ExecutionStateFailed {
				want = models.TurnStateFailed
				kind = EventTypeTurnFailed
			}

			assert.Equal(t, want, turn.State)
			assertRootFinalEvent(t, fixture, kind)
			assert.False(t, fixture.base.store.IsActive(turn.SessionID))
			assertCallbackQueueClosed(t, shared, true)
			callbacks.mutex.Lock()
			assert.Empty(t, callbacks.slots)
			callbacks.mutex.Unlock()
			fixture.base.runtime.userMessageQueuesMutex.Lock()
			assert.Empty(t, fixture.base.runtime.userMessageQueues)
			fixture.base.runtime.userMessageQueuesMutex.Unlock()
			require.NoError(t, callbacks.afterFinish(t.Context(), input,
				result.Output))
			_, err = callbacks.resolve(t.Context(), fixture.input)
			require.ErrorIs(t, err, commerr.ErrInvalidState)
		})
	}
}

func TestRootCallbackFinishRunsThroughRegisteredWorkflow(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("finished"))
	fixture, callbacks, shared := newRootCallbackFixture(t, driver)
	instance := fixture.start(t)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, "finished", result.Text)
	assert.Equal(t, 1, driver.Calls())
	turn, err := fixture.base.store.GetExecutionRootTurn(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, models.TurnStateCompleted, turn.State)
	assertRootFinalEvent(t, fixture, EventTypeTurnCompleted)
	assertCallbackQueueClosed(t, shared, true)
	callbacks.mutex.Lock()
	assert.Empty(t, callbacks.slots)
	callbacks.mutex.Unlock()
	fixture.stop()
}

func TestRootCallbackFinishRefusesLiveAndForeignExecution(t *testing.T) {
	t.Parallel()
	fixture, callbacks, shared := newRootCallbackFixture(t,
		elelemtest.NewScriptedDriver())
	input := callbackFinishInput(fixture)
	require.ErrorIs(t, callbacks.afterFinish(t.Context(), input, input.Result),
		commerr.ErrConflict)
	assertCallbackQueueClosed(t, shared, false)
	input.Execution.SessionID = uuid.New()
	require.ErrorIs(t, callbacks.afterFinish(t.Context(), input, input.Result),
		commerr.ErrPermissionDenied)
	assertCallbackQueueClosed(t, shared, false)
}

func TestRootCallbackFinishRejectsReplacedHolder(t *testing.T) {
	t.Parallel()
	fixture, old, shared := newRootCallbackFixture(t,
		elelemtest.NewScriptedDriver())
	current := newRootWorkflowCallbacks(fixture.base.runtime, uuid.New(), nil)
	replacement, err := current.resolve(t.Context(), fixture.input)
	require.NoError(t, err)
	fixture.engine.options.GenerationID = current.generationID
	fixture.engine.options.BeforeFinish = current.beforeFinish
	fixture.engine.options.AfterFinish = nil
	input := callbackFinishInput(fixture)
	result, err := fixture.engine.finishExecution(t.Context(), input)
	require.NoError(t, err)
	require.ErrorIs(t, old.afterFinish(t.Context(), input, result.Output),
		commerr.ErrConflict)
	assertCallbackQueueClosed(t, replacement, false)
	require.NoError(t, current.afterFinish(t.Context(), input, result.Output))
	assertCallbackQueueClosed(t, replacement, true)
	assertCallbackQueueClosed(t, shared, true)
}

func TestRootCallbackFinishCancelledAdmissionKeepsQueue(t *testing.T) {
	t.Parallel()
	fixture, callbacks, shared := newRootCallbackFixture(t,
		elelemtest.NewScriptedDriver())
	fixture.engine.options.AfterFinish = nil
	input := callbackFinishInput(fixture)
	result, err := fixture.engine.finishExecution(t.Context(), input)
	require.NoError(t, err)
	release, err := shared.gate.acquire(t.Context(), false)
	require.NoError(t, err)
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, callbacks.afterFinish(ctx, input, result.Output),
		context.DeadlineExceeded)
	assertCallbackQueueClosed(t, shared, false)
}

func TestRootCallbackFinishSkipsContinuation(t *testing.T) {
	t.Parallel()
	fixture, _, shared := newRootCallbackFixture(t,
		elelemtest.NewScriptedDriver())
	require.NoError(t, shared.prepared.userMessages.enqueue(t.Context(),
		TurnRequest{RequestID: uuid.New(), Message: "continue retained work"}))
	input := callbackFinishInput(fixture)
	input.Result.AllowContinuation = true
	result, err := fixture.engine.finishExecution(t.Context(), input)
	require.NoError(t, err)
	assert.True(t, result.Output.Continue)
	assertCallbackQueueClosed(t, shared, false)
}
