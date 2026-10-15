package agent

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/hooks"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const nativeRunnerBlockingTool = "nativeRunnerBlock"

func newNativeRunnerFixture(
	t *testing.T,
	driver elelem.Driver,
) runtimeFixture {
	t.Helper()

	return newNativeRunnerFixtureWithOptions(t, driver, nil)
}

func newNativeRunnerFixtureWithOptions(
	t *testing.T,
	driver elelem.Driver,
	customize func(*RuntimeOptions),
) runtimeFixture {
	t.Helper()
	base := newRuntimeFixtureWithOptions(t, driver, customize)
	ctx, cancel := context.WithCancel(t.Context())
	storage, err := db.OpenWorkflowStorage(ctx,
		db.Config{Directory: base.stateDirectory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.Close()) })
	remote, err := storage.WorkerBackend(ctx, uuid.New())
	require.NoError(t, err)
	worker, err := NewWorkflowWorker(ctx, base.runtime, remote, uuid.New(),
		base.runtime.sessionID, nil)
	require.NoError(t, err)
	require.NoError(t, worker.Start(ctx))
	t.Cleanup(func() {
		cancel()
		require.NoError(t, worker.WaitForCompletion())
	})

	return base
}

func runNativeTurnAsync(
	ctx context.Context,
	runtime *Runtime,
) <-chan error {
	finished := make(chan error, 1)
	go func() {
		defer func() {
			if recover() != nil {
				finished <- ctxerrors.New("native runner test panicked")
			}
		}()
		_, err := runtime.Run(ctx, TurnRequest{
			RequestID: uuid.New(), Message: "perform pending operation",
		})
		finished <- err
	}()

	return finished
}

func TestWorkflowRunnerPausesStopAndAllowsUnrelatedWork(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("pendingOperation", nativeRunnerBlockingTool, `{}`),
		elelemtest.Text("unrelated result"))
	base := newNativeRunnerFixture(t, driver)
	var cancelledHooks, completedHooks atomic.Int32
	base.runtime.hookCallback = func(_ context.Context, input hooks.Invocation) (hooks.Outcome, error) {
		if input.Event == harness.HookEventTurnCancelled {
			cancelledHooks.Add(1)
		}
		if input.Event == harness.HookEventTurnStop {
			completedHooks.Add(1)
		}

		return hooks.Outcome{}, nil
	}
	started := make(chan struct{})
	stopped := make(chan struct{})
	base.runtime.extraTools = []elelem.Tool{{
		Name:            nativeRunnerBlockingTool,
		ArgumentsSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, _ elelem.ToolInput) (
			elelem.ToolResult, error,
		) {
			close(started)
			<-ctx.Done()
			close(stopped)

			return elelem.ToolResult{}, ctxerrors.Wrap(ctx.Err(),
				"interrupt pending test operation")
		},
	}}
	finished := runNativeTurnAsync(t.Context(), base.runtime)
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("native tool did not start")
	}
	_, err := base.runtime.CancelSession(t.Context(), base.runtime.sessionID)
	require.NoError(t, err)
	select {
	case err := <-finished:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(10 * time.Second):
		t.Fatal("native turn did not stop")
	}
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("native activity did not stop")
	}
	assert.Zero(t, cancelledHooks.Load())
	assert.Zero(t, completedHooks.Load())
	assert.Equal(t, 1, driver.Calls())
	assertPausedRootAllowsNewWork(t, base)
	assert.Zero(t, cancelledHooks.Load())
	assert.EqualValues(t, 1, completedHooks.Load())
}

func TestWorkflowRunnerFailureCommitsWithoutCompletionHooks(t *testing.T) {
	t.Parallel()
	turns := make([]elelemtest.Turn, 3)
	for index := range turns {
		turns[index].Err = commerr.ErrInvalidArgument
	}
	driver := elelemtest.NewScriptedDriver(turns...)
	base := newNativeRunnerFixture(t, driver)
	var terminalHooks atomic.Int32
	base.runtime.hookCallback = func(_ context.Context, input hooks.Invocation) (hooks.Outcome, error) {
		if input.Event == harness.HookEventTurnStop || input.Event == harness.HookEventTurnCancelled {
			terminalHooks.Add(1)
		}

		return hooks.Outcome{}, nil
	}
	_, err := base.runtime.Run(t.Context(), TurnRequest{RequestID: uuid.New(), Message: "fail this provider request"})
	require.Error(t, err)
	assert.Positive(t, driver.Calls())
	assert.Zero(t, terminalHooks.Load())
	page, err := base.store.SearchExecutions(t.Context(), base.runtime.sessionID, session.SearchExecutionsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	execution := page.Items[0]
	assert.Equal(t, models.ExecutionStateFailed, execution.State)
	assert.NotEmpty(t, execution.FailureDetail)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal([]byte(execution.ContextJSON), &conversation))
	require.NotNil(t, conversation.Root)
	assert.Equal(t, execution.Task, conversation.Root.Message)
	turn, err := base.store.GetExecutionRootTurn(t.Context(), execution.SessionID, execution.ID)
	require.NoError(t, err)
	assert.Equal(t, models.TurnStateFailed, turn.State)
	events, err := base.store.ListEvents(t.Context(), execution.SessionID, session.ListEventsOptions{})
	require.NoError(t, err)
	failed := 0
	for _, event := range events.Items {
		if event.EventType == EventTypeTurnFailed {
			failed++
		}
	}
	assert.Equal(t, 1, failed)
}

func assertPausedRootAllowsNewWork(t *testing.T, base runtimeFixture) {
	t.Helper()
	page, err := base.store.SearchExecutions(t.Context(), base.runtime.sessionID,
		session.SearchExecutionsOptions{State: models.ExecutionStatePaused})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	paused := page.Items[0]
	result, err := base.runtime.Run(t.Context(), TurnRequest{
		RequestID: uuid.New(), Message: "do unrelated work",
	})
	require.NoError(t, err)
	assert.Equal(t, "unrelated result", result.Text)
	retained, err := base.store.GetExecution(t.Context(), paused.SessionID,
		paused.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, retained.State)
	assert.Equal(t, paused.ControlRevision, retained.ControlRevision)
}

func TestWorkflowRunnerRejectsDuplicateBinding(t *testing.T) {
	t.Parallel()
	base := newNativeRunnerFixture(t, elelemtest.NewScriptedDriver())
	original := base.runtime.nativeWorkflowRunner()
	err := base.runtime.bindWorkflowRunner(nil, nil, nil, base.runtime.sessionID)
	require.ErrorIs(t, err, commerr.ErrConflict)
	assert.Same(t, original, base.runtime.nativeWorkflowRunner())
	err = base.runtime.bindWorkflowRunner(nil, nil, nil, uuid.New())
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	assert.Same(t, original, base.runtime.nativeWorkflowRunner())
}

func TestWorkflowRunnerCallerInterruptionDoesNotPause(t *testing.T) {
	t.Parallel()
	base := newNativeRunnerFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("retainedOperation", nativeRunnerBlockingTool, `{}`),
		elelemtest.Text("retained completion")))
	started := make(chan struct{})
	release := make(chan struct{})
	completed := make(chan struct{})
	base.runtime.observeEvent = func(_ uuid.UUID, event Event) {
		if event.Type == EventTypeTurnCompleted {
			close(completed)
		}
	}
	base.runtime.extraTools = []elelem.Tool{{
		Name:            nativeRunnerBlockingTool,
		ArgumentsSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, _ elelem.ToolInput) (
			elelem.ToolResult, error,
		) {
			close(started)
			select {
			case <-release:
				return elelem.ToolResult{Content: "retained operation"}, nil
			case <-ctx.Done():
				return elelem.ToolResult{}, ctxerrors.Wrap(ctx.Err(),
					"interrupt retained test operation")
			}
		},
	}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := runNativeTurnAsync(ctx, base.runtime)
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("native retained tool did not start")
	}
	cancel()
	select {
	case err := <-finished:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(10 * time.Second):
		t.Fatal("interrupted native caller did not return")
	}
	close(release)
	select {
	case <-completed:
	case <-time.After(10 * time.Second):
		t.Fatal("native execution did not continue after caller interruption")
	}
	assertSingleCompletedNativeExecution(t, base)
}

func assertSingleCompletedNativeExecution(t *testing.T, base runtimeFixture) {
	t.Helper()
	page, err := base.store.SearchExecutions(t.Context(), base.runtime.sessionID,
		session.SearchExecutionsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.ExecutionStateCompleted, page.Items[0].State)
	assert.Zero(t, page.Items[0].ControlRevision)
}

func TestWorkflowRunnerRejectsMalformedResultContext(t *testing.T) {
	t.Parallel()
	_, err := nativeRootTurnResult(nil, workflowRoundResult{
		Context: json.RawMessage("invalid context"),
	})
	var syntax *json.SyntaxError
	require.ErrorAs(t, err, &syntax)
}

func TestWorkflowRunnerUsesNativeRootAndPreservesResult(t *testing.T) {
	t.Parallel()
	base := newNativeRunnerFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Thinking("saved reasoning", "native runner result").
			WithUsage(elelem.Usage{TokenCounts: elelem.TokenCounts{
				Prompt: 11, Completion: 7,
			}})))
	result, err := base.runtime.Run(t.Context(), TurnRequest{
		RequestID: uuid.New(), Message: "inspect workspace",
	})
	require.NoError(t, err)
	assert.Equal(t, "native runner result", result.Text)
	assert.Equal(t, "saved reasoning", result.Thinking)
	assert.Equal(t, elelem.FinishReasonStop, result.FinishReason)
	assert.Equal(t, int64(7), result.OutputTokens)
	assert.False(t, result.HasToolCalls)
	page, err := base.store.SearchExecutions(t.Context(), result.SessionID,
		session.SearchExecutionsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, agentWorkflowName, page.Items[0].WorkflowVersion)
	assert.Equal(t, models.ExecutionStateCompleted, page.Items[0].State)
	require.NotEmpty(t, result.Events)
	assert.Equal(t, EventTypeTurnCompleted,
		result.Events[len(result.Events)-1].Type)
}
