package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cschleiden/go-workflows/client"
	"github.com/cschleiden/go-workflows/workflow"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	workflowWaitTreeSize    = 8
	workflowWaitLeafMessage = "resume this selected leaf"
	workflowWaitLeafResult  = "tree wait finished"
)

func waitingNativeTreeDriver(t *testing.T) nativeTreeDriver {
	t.Helper()
	driver := nativeParallelTreeDriver(t)
	children := map[string][]string{
		"branchA": {"leafA", "leafB", "leafC"},
		"branchB": {"leafD"}, "branchC": nil,
		"leafA": nil, "leafB": nil, "leafC": nil, "leafD": nil,
	}
	for name, names := range children {
		turns := []elelemtest.Turn{}
		if len(names) != 0 {
			turns = append(turns, nativeTreeLaunches(t, names))
		}
		turns = append(turns, elelemtest.ToolCall("waitTreeMessages",
			toolNameWaitMessages, `{"timeout":"1h"}`),
			elelemtest.Text(workflowWaitLeafResult))
		driver.routes[nativeTreeRoute(name)] = elelemtest.NewScriptedDriver(turns...)
	}

	return driver
}

func TestWorkflowWorkerPausesEightAgentWaitTree(t *testing.T) {
	t.Parallel()
	driver := waitingNativeTreeDriver(t)
	freshNames := []string{
		"freshA", "freshB", "freshC", "freshD", "freshE", "freshF", "freshG",
	}
	driver.ScriptedDriver = elelemtest.NewScriptedDriver(
		nativeTreeLaunches(t, []string{"branchA", "branchB", "branchC"}),
		elelemtest.Text("tree root finished"),
		nativeTreeLaunches(t, freshNames),
		elelemtest.Text("new root finished"))
	for _, name := range freshNames {
		driver.routes[nativeTreeRoute(name)] = elelemtest.NewScriptedDriver(
			elelemtest.ToolCall("freshWait", toolNameWaitMessages,
				`{"timeout":"1h"}`))
	}
	fixture, _ := newRootModelWorkflowFixtureWithOptions(t, driver, func(options *RuntimeOptions) {
		options.AgentLimits.MaxConcurrentRuns = workflowWaitTreeSize - 1
	})
	ctx, cancel := context.WithCancel(t.Context())
	remote, err := fixture.storage.WorkerBackend(ctx, uuid.New())
	require.NoError(t, err)
	assembled, err := NewWorkflowWorker(ctx, fixture.base.runtime, remote,
		fixture.engine.options.GenerationID, fixture.input.SessionID, nil)
	require.NoError(t, err)
	require.NoError(t, assembled.Start(ctx))
	t.Cleanup(func() {
		cancel()
		require.NoError(t, assembled.WaitForCompletion())
	})
	api := client.New(remote)
	instance, err := api.CreateWorkflowInstance(ctx,
		client.WorkflowInstanceOptions{
			InstanceID: fmt.Sprintf("%s/%s", fixture.input.SessionID,
				fixture.input.ExecutionID),
			Queue: workflow.Queue(fixture.input.SessionID.String()),
		}, agentWorkflowName, fixture.input)
	require.NoError(t, err)
	_, err = client.GetWorkflowResult[workflowRoundResult](ctx, api,
		instance, workflowTestResultTimeout)
	require.NoError(t, err)
	var items []*models.Execution
	require.Eventually(t, func() bool {
		page, err := fixture.base.store.SearchExecutions(ctx,
			fixture.input.SessionID, session.SearchExecutionsOptions{})
		if err != nil || len(page.Items) != workflowWaitTreeSize {
			return false
		}
		items = page.Items

		return true
	}, 30*time.Second, 10*time.Millisecond)
	for _, item := range items {
		require.Equal(t, agentWorkflowName, item.WorkflowVersion)
	}
	waits := awaitWorkerTreeWaits(t, fixture, items)
	result, err := fixture.base.runtime.CancelSession(ctx, fixture.input.SessionID)
	require.NoError(t, err)
	assert.True(t, result.CancelRequested)
	paused, err := fixture.base.runtime.PauseSessionWorkflows(ctx)
	require.NoError(t, err)
	assert.True(t, paused)
	assertWorkerTreeWaitsPaused(t, fixture, waits)
	for _, item := range items {
		stored, err := fixture.base.store.GetExecution(ctx,
			fixture.input.SessionID, item.ID)
		require.NoError(t, err)
		if item.Kind == models.ExecutionKindRoot {
			assert.Equal(t, models.ExecutionStateCompleted, stored.State)
		} else {
			assert.Equal(t, models.ExecutionStatePaused, stored.State)
		}
		assert.Equal(t, item.RootTurnID, stored.RootTurnID)
		assert.Equal(t, item.ParentExecutionID, stored.ParentExecutionID)
		assert.Equal(t, item.AgentRunID, stored.AgentRunID)
		assert.Equal(t, item.WorkingDirectory, stored.WorkingDirectory)
	}
	counts := make(map[string]int, len(driver.routes))
	for route, child := range driver.routes {
		counts[route] = child.Calls()
	}
	_, err = fixture.base.runtime.Run(ctx, TurnRequest{
		RequestID: uuid.New(), Message: "perform unrelated work after tree stop",
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		for _, name := range freshNames {
			if driver.routes[nativeTreeRoute(name)].Calls() != 1 {
				return false
			}
		}

		return true
	}, 15*time.Second, 10*time.Millisecond,
		"paused children must release capacity for fresh child work")
	for route, child := range driver.routes {
		if counts[route] == 0 {
			continue
		}
		assert.Equal(t, counts[route], child.Calls())
	}
	assertWorkerTreeWaitsPaused(t, fixture, waits)
	refuseWorkerTreeResumeAtCapacity(t, fixture, api, driver, waits)
	resumeWorkerTreeLeaf(t, fixture, api, driver, items, waits)
}

func workerTreeExecutionNamed(
	t *testing.T,
	fixture executionLoopFixture,
	name string,
) *models.Execution {
	t.Helper()
	page, err := fixture.base.store.SearchExecutions(t.Context(),
		fixture.input.SessionID, session.SearchExecutionsOptions{})
	require.NoError(t, err)
	for _, item := range page.Items {
		if item.AgentRunID == nil {
			continue
		}
		run, err := fixture.base.store.GetAgentRun(t.Context(), item.SessionID,
			*item.AgentRunID)
		require.NoError(t, err)
		if run.Name == name {
			return item
		}
	}
	t.Fatalf("missing child %s", name)

	return nil
}

func refuseWorkerTreeResumeAtCapacity(
	t *testing.T,
	fixture executionLoopFixture,
	api *client.Client,
	driver nativeTreeDriver,
	waits map[uuid.UUID]*models.ExecutionActivity,
) {
	t.Helper()
	leaf := workerTreeExecutionNamed(t, fixture, "leafA")
	provider := driver.routes[nativeTreeRoute("leafA")]
	calls := provider.Calls()
	_, err := fixture.base.store.ResumeExecution(t.Context(), leaf.SessionID,
		leaf.ID)
	require.NoError(t, err)
	require.NoError(t, api.SignalWorkflow(t.Context(),
		fmt.Sprintf("%s/%s", leaf.SessionID, leaf.ID), workflowResumeSignal,
		struct{}{}))
	require.Eventually(t, func() bool {
		stored, err := fixture.base.store.GetExecution(t.Context(),
			leaf.SessionID, leaf.ID)
		if err != nil || stored.State != models.ExecutionStatePaused {
			return false
		}
		activity, err := fixture.base.store.GetExecutionActivity(t.Context(),
			leaf.SessionID, waits[leaf.ID].ID)

		return err == nil && activity.State == models.ActivityStatePaused
	}, 15*time.Second, 10*time.Millisecond)
	stored, err := fixture.base.store.GetExecution(t.Context(), leaf.SessionID,
		leaf.ID)
	require.NoError(t, err)
	assert.Equal(t, leaf.ContextJSON, stored.ContextJSON)
	assert.Equal(t, calls, provider.Calls())
	activity, err := fixture.base.store.GetExecutionActivity(t.Context(),
		leaf.SessionID, waits[leaf.ID].ID)
	require.NoError(t, err)
	assert.Equal(t, models.ActivityStatePaused, activity.State)
	assert.Equal(t, waits[leaf.ID].InputJSON, activity.InputJSON)
	waits[leaf.ID] = activity
	fresh := workerTreeExecutionNamed(t, fixture, "freshA")
	_, err = fixture.base.runtime.CancelSessionAgentRun(t.Context(),
		fresh.SessionID, *fresh.AgentRunID)
	require.NoError(t, err)
}

func resumeWorkerTreeLeaf(
	t *testing.T,
	fixture executionLoopFixture,
	api *client.Client,
	driver nativeTreeDriver,
	items []*models.Execution,
	waits map[uuid.UUID]*models.ExecutionActivity,
) {
	t.Helper()
	var leaf *models.Execution
	var leafRun *models.AgentRun
	for _, item := range items {
		if item.AgentRunID == nil {
			continue
		}
		run, err := fixture.base.store.GetAgentRun(t.Context(), item.SessionID,
			*item.AgentRunID)
		require.NoError(t, err)
		if run.Name == "leafA" {
			leaf = item
			leafRun = run

			break
		}
	}
	require.NotNil(t, leaf)
	before := waits[leaf.ID]
	require.NotNil(t, before)
	var retained workflowToolPhaseInput
	require.NoError(t, json.Unmarshal([]byte(before.InputJSON), &retained))
	require.NotNil(t, retained.State.Wait)
	rootCalls := driver.Calls()
	counts := make(map[string]int, len(driver.routes))
	for route, child := range driver.routes {
		counts[route] = child.Calls()
	}
	changed, err := fixture.base.store.ResumeExecution(t.Context(),
		leaf.SessionID, leaf.ID)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{leaf.ID}, changed)
	_, err = fixture.base.store.AcceptChildExecutionMessage(t.Context(),
		leaf.SessionID, leaf.ID, session.AcceptExecutionMessageInput{
			RequestID: uuid.New(), WorkingDirectory: leaf.WorkingDirectory,
			Message:  workflowWaitLeafMessage,
			Capacity: fixture.base.runtime.maxQueuedUserMessages,
		})
	require.NoError(t, err)
	require.NoError(t, api.SignalWorkflow(t.Context(),
		fmt.Sprintf("%s/%s", leaf.SessionID, leaf.ID),
		workflowResumeSignal, struct{}{}))
	require.Eventually(t, func() bool {
		stored, err := fixture.base.store.GetExecution(t.Context(),
			leaf.SessionID, leaf.ID)

		return err == nil && stored.State == models.ExecutionStateCompleted
	}, 20*time.Second, 10*time.Millisecond)
	instance, err := fixture.base.runtime.nativeWorkflowRunner().api.
		CreateWorkflowInstance(t.Context(), client.WorkflowInstanceOptions{
			InstanceID: fmt.Sprintf("%s/%s", leaf.SessionID, leaf.ID),
		}, leaf.WorkflowVersion, workflowExecutionInput{})
	require.NoError(t, err)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, workflowWaitLeafResult, result.Text)
	assert.Equal(t, rootCalls, driver.Calls())
	executions, err := fixture.base.store.SearchExecutions(t.Context(),
		leaf.SessionID, session.SearchExecutionsOptions{})
	require.NoError(t, err)
	assert.Len(t, executions.Items, workflowWaitTreeSize*2)
	after, err := fixture.base.store.GetExecutionActivity(t.Context(),
		leaf.SessionID, before.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ActivityStateCompleted, after.State)
	assert.Equal(t, before.Attempt+1, after.Attempt)
	require.NotNil(t, after.OutputJSON)
	var completed workflowToolPhaseState
	require.NoError(t, json.Unmarshal([]byte(*after.OutputJSON), &completed))
	require.NotNil(t, completed.Wait)
	assert.Equal(t, retained.State.Wait.ID, completed.Wait.ID)
	assert.Equal(t, retained.State.Wait.Deadline, completed.Wait.Deadline)
	savedRun, err := fixture.base.store.GetAgentRun(t.Context(), leaf.SessionID,
		*leaf.AgentRunID)
	require.NoError(t, err)
	assert.Equal(t, models.AgentRunStateCompleted, savedRun.State)
	assert.Equal(t, leafRun.StartedAt, savedRun.StartedAt)
	assert.Equal(t, leafRun.ParentAgentRunID, savedRun.ParentAgentRunID)
	assert.Equal(t, leafRun.ParentToolCallID, savedRun.ParentToolCallID)
	messages, err := fixture.base.store.ListAgentRunMessages(t.Context(),
		leaf.SessionID, *leaf.AgentRunID, session.ListAgentRunMessagesOptions{})
	require.NoError(t, err)
	delivered := 0
	for _, message := range messages.Items {
		if message.Content == workflowWaitLeafMessage {
			delivered++
		}
	}
	assert.Equal(t, 1, delivered)
	for route, child := range driver.routes {
		want := counts[route]
		if route == nativeTreeRoute("leafA") {
			want++
		}
		assert.Equal(t, want, child.Calls())
	}
	remaining := make(map[uuid.UUID]*models.ExecutionActivity, len(waits)-1)
	for id, activity := range waits {
		if id != leaf.ID {
			remaining[id] = activity
		}
	}
	assertWorkerTreeWaitsPaused(t, fixture, remaining)
}

func awaitWorkerTreeWaits(
	t *testing.T,
	fixture executionLoopFixture,
	items []*models.Execution,
) map[uuid.UUID]*models.ExecutionActivity {
	t.Helper()
	waits := make(map[uuid.UUID]*models.ExecutionActivity)
	require.Eventually(t, func() bool {
		for _, item := range items {
			if item.Kind == models.ExecutionKindRoot {
				continue
			}
			page, err := fixture.base.store.ListExecutionActivities(t.Context(),
				item.SessionID, item.ID, session.ListEventsOptions{})
			if err != nil {
				return false
			}
			for _, activity := range page.Items {
				if activity.Name != toolPhaseActivityName ||
					activity.State != models.ActivityStateRunning {
					continue
				}
				var input workflowToolPhaseInput
				if json.Unmarshal([]byte(activity.InputJSON), &input) != nil ||
					input.Phase != workflowToolPhaseHandler ||
					input.Tool.Call.Name != toolNameWaitMessages {
					continue
				}
				waits[item.ID] = activity
			}
		}

		return len(waits) == workflowWaitTreeSize-1
	}, 30*time.Second, 10*time.Millisecond)

	return waits
}

func assertWorkerTreeWaitsPaused(
	t *testing.T,
	fixture executionLoopFixture,
	waits map[uuid.UUID]*models.ExecutionActivity,
) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, activity := range waits {
			stored, err := fixture.base.store.GetExecutionActivity(t.Context(),
				fixture.input.SessionID, activity.ID)
			if err != nil || stored.State != models.ActivityStatePaused {
				return false
			}
		}

		return true
	}, 20*time.Second, 10*time.Millisecond)
	for id, before := range waits {
		stored, err := fixture.base.store.GetExecutionActivity(t.Context(),
			fixture.input.SessionID, before.ID)
		require.NoError(t, err)
		assert.Equal(t, before.InputJSON, stored.InputJSON)
		assert.Equal(t, before.ContextJSON, stored.ContextJSON)
		assert.Nil(t, stored.OutputJSON)
		execution, err := fixture.base.store.GetExecution(t.Context(),
			fixture.input.SessionID, id)
		require.NoError(t, err)
		assert.Equal(t, models.ExecutionStatePaused, execution.State)
	}
}

func TestWorkflowWorkerRunsNativeRoot(t *testing.T) {
	t.Parallel()
	assertWorkflowWorkerRoot(t)
}

func assertWorkflowWorkerRoot(t *testing.T) {
	t.Helper()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("workerRead", toolNameReadFile,
			`{"path":"worker.txt"}`), elelemtest.Text("worker result"))
	fixture, prepared := newRootModelWorkflowFixture(t, driver)
	writeRuntimeFile(t, filepath.Join(fixture.base.workspace, "worker.txt"),
		"native worker source")
	ctx, cancel := context.WithCancel(t.Context())
	remote, err := fixture.storage.WorkerBackend(ctx, uuid.New())
	require.NoError(t, err)
	var events atomic.Int64

	assembled, err := NewWorkflowWorker(ctx, fixture.base.runtime, remote,
		fixture.engine.options.GenerationID, fixture.input.SessionID,
		func(Event) error {
			events.Add(1)

			return nil
		})
	require.NoError(t, err)
	require.NoError(t, assembled.Start(ctx))
	t.Cleanup(func() {
		cancel()
		require.NoError(t, assembled.WaitForCompletion())
	})
	api := client.New(remote)
	instance, err := api.CreateWorkflowInstance(ctx,
		client.WorkflowInstanceOptions{
			InstanceID: fmt.Sprintf("%s/%s", fixture.input.SessionID,
				fixture.input.ExecutionID),
			Queue: workflow.Queue(fixture.input.SessionID.String()),
		}, agentWorkflowName, fixture.input)
	require.NoError(t, err)
	result, err := client.GetWorkflowResult[workflowRoundResult](ctx, api,
		instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, "worker result", result.Text)
	turn, err := fixture.base.store.GetExecutionRootTurn(ctx,
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, prepared.lease.TurnID, turn.ID)
	assert.Equal(t, models.TurnStateCompleted, turn.State)
	assert.Equal(t, 2, driver.Calls())
	assert.Positive(t, events.Load())
}

func TestWorkflowWorkerRefusesMissingDependencies(t *testing.T) {
	t.Parallel()
	fixture, _ := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	remote, err := fixture.storage.WorkerBackend(t.Context(), uuid.New())
	require.NoError(t, err)
	testCases := []struct {
		name         string
		runtime      *Runtime
		generationID uuid.UUID
		sessionID    uuid.UUID
	}{
		{name: "runtime", generationID: uuid.New(), sessionID: uuid.New()},
		{
			name: "storage", runtime: &Runtime{},
			generationID: uuid.New(), sessionID: uuid.New(),
		},
		{
			name: "generation", runtime: fixture.base.runtime,
			sessionID: uuid.New(),
		},
		{
			name: "session", runtime: fixture.base.runtime,
			generationID: uuid.New(),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			worker, err := NewWorkflowWorker(t.Context(), tc.runtime, remote,
				tc.generationID, tc.sessionID, nil)
			require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
			assert.Nil(t, worker)
		})
	}

	worker, err := NewWorkflowWorker(t.Context(), fixture.base.runtime, nil,
		uuid.New(), uuid.New(), nil)
	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
	assert.Nil(t, worker)
}

func TestWorkflowWorkerRefusesCancelledAssembly(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	worker, err := NewWorkflowWorker(ctx, &Runtime{}, nil,
		uuid.New(), uuid.New(), nil)
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, worker)
}
