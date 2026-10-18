package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cschleiden/go-workflows/client"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newRootCallbackFixture(
	t *testing.T,
	driver *elelemtest.ScriptedDriver,
) (executionLoopFixture, *rootWorkflowCallbacks, *rootCallbackScope) {
	t.Helper()
	fixture, _ := newRootModelWorkflowFixture(t, driver)
	callbacks := newRootWorkflowCallbacks(fixture.base.runtime,
		fixture.engine.options.GenerationID, nil)
	callbacks.bind(&fixture.engine.options)
	shared, err := callbacks.resolve(t.Context(), fixture.input)
	require.NoError(t, err)
	t.Cleanup(func() {
		fixture.base.runtime.closeActiveUserMessageQueue(shared.prepared)
	})

	return fixture, callbacks, shared
}

func TestRootCallbacksConcurrentResolutionKeepsQueueAndHolder(t *testing.T) {
	t.Parallel()
	fixture, callbacks, first := newRootCallbackFixture(t,
		elelemtest.NewScriptedDriver())
	_, arrived := first.prepared.userMessages.arrival()
	var group sync.WaitGroup

	for range 8 {
		group.Go(func() {
			shared, err := callbacks.resolve(t.Context(), fixture.input)
			assert.NoError(t, err)
			assert.Same(t, first, shared)
		})
	}

	group.Wait()
	_, after := first.prepared.userMessages.arrival()
	assert.Equal(t, arrived, after)
	page, err := fixture.base.store.ListExecutionEvents(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	restored := 0
	for _, event := range page.Items {
		if event.EventType == string(session.ExecutionEventTypeTurnRestored) {
			restored++
		}
	}
	assert.Equal(t, 1, restored)
}

func TestRootCallbacksPauseResumeReplacesHolderWithoutPromotingOldCallbacks(
	t *testing.T,
) {
	t.Parallel()
	fixture, callbacks, before := newRootCallbackFixture(t,
		elelemtest.NewScriptedDriver())
	_, err := fixture.base.store.PauseExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID, "retain paused work")
	require.NoError(t, err)
	_, err = callbacks.resolve(t.Context(), fixture.input)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	_, err = fixture.base.store.ResumeExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	after, err := callbacks.resolve(t.Context(), fixture.input)
	require.NoError(t, err)
	assert.NotSame(t, before, after)
	assert.NotEqual(t, before.prepared.lease.ID, after.prepared.lease.ID)
	require.ErrorIs(t, fixture.base.store.AppendCheckpoint(t.Context(),
		before.prepared.lease, nil, nil), commerr.ErrInvalidState)
	require.NoError(t, fixture.base.store.AppendCheckpoint(t.Context(),
		after.prepared.lease, nil, nil))
}

func TestRootCallbacksRejectAnotherHolderAndDoNotStealIt(t *testing.T) {
	t.Parallel()
	fixture, callbacks, before := newRootCallbackFixture(t,
		elelemtest.NewScriptedDriver())
	_, err := fixture.base.store.RestoreExecutionTurn(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.RestoreExecutionTurnInput{
			OwnerGenerationID: uuid.New(), PreviousLeaseID: before.prepared.lease.ID,
		})
	require.NoError(t, err)
	_, err = callbacks.resolve(t.Context(), fixture.input)
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestRootCallbacksNativeParallelReadsRefreshMergedState(t *testing.T) {
	t.Parallel()
	paths := []string{"first.txt", "second.txt"}
	deltas := make([]elelem.Delta, len(paths))
	for index, path := range paths {
		deltas[index].ToolCall = &elelem.ToolCallDelta{
			Index: index, ID: path, Name: toolNameReadFile,
			Arguments: runtimeToolArguments(t, path),
		}
	}
	deltas[len(deltas)-1].FinishReason = elelem.FinishReasonToolCalls
	driver := elelemtest.NewScriptedDriver(elelemtest.Turn{Deltas: deltas},
		elelemtest.Text(runtimeToolFinalText))
	fixture, _, shared := newRootCallbackFixture(t, driver)
	fixture.input.MaxConcurrentTools = 2
	for _, path := range paths {
		writeRuntimeFile(t, filepath.Join(fixture.base.workspace, path), path)
	}
	queue := shared.prepared.userMessages
	orderRootWorkflowToolSnapshots(t, &fixture)
	instance := fixture.start(t)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(result.Context, &conversation))
	require.Len(t, conversation.Runtime.Observations, 2)
	assert.Same(t, queue, shared.prepared.userMessages)
	assert.Equal(t, 2, driver.Calls())
	fixture.stop()
}

func TestRootCallbacksCancelledScopeAdmission(t *testing.T) {
	t.Parallel()
	fixture, callbacks, shared := newRootCallbackFixture(t,
		elelemtest.NewScriptedDriver())
	reader, err := shared.gate.acquire(t.Context(), false)
	require.NoError(t, err)
	t.Cleanup(reader)
	ctx, cancel := context.WithTimeout(t.Context(), rootCallbackGateTestTimeout)
	t.Cleanup(cancel)
	_, _, err = callbacks.acquire(ctx, fixture.input, true)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestRootCallbacksConcurrentColdConstructionCreatesOneHolder(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver()
	fixture, _ := newRootModelWorkflowFixture(t, driver)
	callbacks := newRootWorkflowCallbacks(fixture.base.runtime,
		fixture.engine.options.GenerationID, nil)
	results := make(chan *rootCallbackScope, 8)
	var group sync.WaitGroup

	for range 8 {
		group.Go(func() {
			shared, err := callbacks.resolve(t.Context(), fixture.input)
			assert.NoError(t, err)
			results <- shared
		})
	}

	group.Wait()
	close(results)
	var first *rootCallbackScope

	for shared := range results {
		require.NotNil(t, shared)
		if first == nil {
			first = shared
		}

		assert.Same(t, first, shared)
	}

	assert.Zero(t, driver.Calls())
	page, err := fixture.base.store.ListExecutionEvents(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 2)
}

func TestRootCallbacksDirectoryHopRefreshKeepsTargetPrompt(t *testing.T) {
	t.Parallel()
	target := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(target, "AGENTS.md"),
		[]byte("Target scope instructions."), runtimeTestFileMode))
	arguments, err := json.Marshal(changeDirectoryInput{Directory: target})
	require.NoError(t, err)
	driver := elelemtest.NewScriptedDriver(elelemtest.ToolCall("hopCall",
		toolNameChangeDirectory, string(arguments)), elelemtest.Text("finished"))
	fixture, _, shared := newRootCallbackFixture(t, driver)
	instance := fixture.start(t)
	_, err = client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	requests := driver.Requests()
	require.Len(t, requests, 2)
	assert.Contains(t, requests[1].Messages[0].Text(),
		"Target scope instructions.")
	assert.Equal(t, target, shared.prepared.workspace)
	fixture.stop()
}

func TestColdRootWorkflowWaitsForSharedTurnCapacity(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("cold root finished"))
	fixture, _ := newRootModelWorkflowFixture(t, driver)
	fixture.base.runtime.turnSlots = newTurnSlots(1)
	held, err := fixture.base.runtime.acquireTurnSlot(t.Context())
	require.NoError(t, err)
	release := sync.OnceFunc(held)
	t.Cleanup(release)
	callbacks := newRootWorkflowCallbacks(fixture.base.runtime, fixture.engine.options.GenerationID, nil)
	callbacks.bind(&fixture.engine.options)
	entered := make(chan struct{})
	prepared := make(chan struct{})
	var once, ready sync.Once
	opening := fixture.engine.options.OpeningHook
	fixture.engine.options.OpeningHook = func(ctx context.Context, input workflowOpeningInput) (workflowOpeningResult, error) {
		once.Do(func() { close(entered) })
		result, err := opening(ctx, input)
		if err == nil {
			ready.Do(func() { close(prepared) })
		}

		return result, err
	}
	instance := fixture.start(t)
	select {
	case <-entered:
	case <-time.After(workflowTestResultTimeout):
		t.Fatal("cold preparation did not start")
	}
	assert.Never(t, func() bool {
		select {
		case <-prepared:
			return true
		default:
			return false
		}
	}, 500*time.Millisecond, 10*time.Millisecond)
	assert.Zero(t, driver.Calls())
	release()
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(), fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, "cold root finished", result.Text)
	assert.Empty(t, fixture.base.runtime.turnSlots)
	assert.Equal(t, 1, driver.Calls())
}

func TestRootCallbackPauseAndResumeReturnSharedCapacity(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		limit int
	}{{name: "bounded", limit: 1}, {name: "unlimited", limit: -1}}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, _ := newRootModelWorkflowFixture(t, elelemtest.NewScriptedDriver())
			runtime := fixture.base.runtime
			runtime.turnSlots = newTurnSlots(tc.limit)
			callbacks := newRootWorkflowCallbacks(runtime, fixture.engine.options.GenerationID, nil)
			for range 2 {
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				shared, err := callbacks.resolve(ctx, fixture.input)
				cancel()
				require.NoError(t, err)
				reused, err := callbacks.resolve(t.Context(), fixture.input)
				require.NoError(t, err)
				assert.Same(t, shared, reused)
				_, err = fixture.base.store.PauseExecution(t.Context(), fixture.input.SessionID, fixture.input.ExecutionID, "retain work")
				require.NoError(t, err)
				stored, err := fixture.base.store.GetExecution(t.Context(), fixture.input.SessionID, fixture.input.ExecutionID)
				require.NoError(t, err)
				require.NoError(t, callbacks.retirePaused(t.Context(), fixture.base.store, stored))
				require.NoError(t, callbacks.retirePaused(t.Context(), fixture.base.store, stored))
				ctx, cancel = context.WithTimeout(t.Context(), time.Second)
				release, err := runtime.acquireTurnSlot(ctx)
				cancel()
				require.NoError(t, err)
				release()
				_, err = fixture.base.store.ResumeExecution(t.Context(), fixture.input.SessionID, fixture.input.ExecutionID)
				require.NoError(t, err)
			}
		})
	}
}

func TestRootCallbackConstructionFailureReturnsCapacity(t *testing.T) {
	t.Parallel()
	fixture, _ := newRootModelWorkflowFixture(t, elelemtest.NewScriptedDriver())
	runtime := fixture.base.runtime
	runtime.turnSlots = newTurnSlots(1)
	_, err := fixture.base.store.RestoreExecutionTurn(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.RestoreExecutionTurnInput{OwnerGenerationID: uuid.New()})
	require.NoError(t, err)

	callbacks := newRootWorkflowCallbacks(runtime, fixture.engine.options.GenerationID, nil)
	_, err = callbacks.construct(t.Context(), fixture.base.store, callbacks.slot(fixture.input.ExecutionID), fixture.input,
		&models.ExecutionTurnLease{LeaseID: uuid.New()})
	require.ErrorIs(t, err, commerr.ErrConflict)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	shared, err := callbacks.resolve(ctx, fixture.input)
	require.NoError(t, err)
	runtime.closeActiveUserMessageQueue(shared.prepared)
	shared.releaseTurnCapacity()
}
