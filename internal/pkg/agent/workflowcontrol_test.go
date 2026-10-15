package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cschleiden/go-workflows/backend/history"
	"github.com/cschleiden/go-workflows/client"
	"github.com/cschleiden/go-workflows/core"
	"github.com/cschleiden/go-workflows/workflow"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootAdmissionRefusalClassification(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		kind  models.ExecutionKind
		state models.ExecutionState
		cause error
		want  bool
	}{
		{name: "queued root", kind: models.ExecutionKindRoot, state: models.ExecutionStateQueued, cause: errRootAdmissionBusy, want: true},
		{name: "running root", kind: models.ExecutionKindRoot, state: models.ExecutionStateRunning, cause: errRootAdmissionBusy, want: true},
		{name: "reconciling root", kind: models.ExecutionKindRoot, state: models.ExecutionStateReconciling, cause: errRootAdmissionBusy, want: true},
		{name: "paused root", kind: models.ExecutionKindRoot, state: models.ExecutionStatePaused, cause: errRootAdmissionBusy, want: true},
		{name: "completed root", kind: models.ExecutionKindRoot, state: models.ExecutionStateCompleted, cause: errRootAdmissionBusy},
		{name: "failed root", kind: models.ExecutionKindRoot, state: models.ExecutionStateFailed, cause: errRootAdmissionBusy},
		{name: "cancelled root", kind: models.ExecutionKindRoot, state: models.ExecutionStateCancelled, cause: errRootAdmissionBusy},
		{name: "child", kind: models.ExecutionKindChild, state: models.ExecutionStateRunning, cause: errRootAdmissionBusy},
		{name: "other lock", kind: models.ExecutionKindRoot, state: models.ExecutionStateRunning, cause: commerr.ErrLockHeld},
		{name: "no error", kind: models.ExecutionKindRoot, state: models.ExecutionStateRunning},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, rootAdmissionRefused(&models.Execution{Kind: tc.kind, State: tc.state}, tc.cause))
		})
	}
}

func TestNativeChildCancellationStopsOnlySelectedSDKSubtree(t *testing.T) {
	t.Parallel()
	driver := waitingNativeTreeDriver(t)
	base := newNativeRunnerFixtureWithOptions(t, driver,
		func(options *RuntimeOptions) {
			options.AgentLimits.MaxConcurrentRuns = workflowWaitTreeSize
		})
	_, err := base.runtime.Run(t.Context(), TurnRequest{
		Message: "launch parallel child work",
	})
	require.NoError(t, err)
	var items []*models.Execution
	require.Eventually(t, func() bool {
		page, err := base.store.SearchExecutions(t.Context(),
			base.runtime.sessionID, session.SearchExecutionsOptions{})
		if err != nil || len(page.Items) != workflowWaitTreeSize {
			return false
		}
		items = page.Items

		return true
	}, 20*time.Second, 10*time.Millisecond)
	waits := awaitWorkerTreeWaits(t, executionLoopFixture{
		base:  base,
		input: workflowExecutionInput{SessionID: base.runtime.sessionID},
	}, items)
	var target *models.Execution
	for _, item := range items {
		if item.AgentRunID == nil {
			continue
		}
		run, err := base.store.GetAgentRun(t.Context(), item.SessionID,
			*item.AgentRunID)
		require.NoError(t, err)
		if run.Name == "branchA" {
			target = item
		}
	}
	require.NotNil(t, target)
	result, err := base.runtime.CancelSessionAgentRun(t.Context(),
		target.SessionID, *target.AgentRunID)
	require.NoError(t, err)
	assert.True(t, result.CancelRequested)
	assert.Equal(t, string(models.AgentRunStateCancelled), string(result.State))
	assertCancelledNativeTree(t, base, target, items, waits)
	for _, item := range items {
		if item.ID != target.ID && (item.ParentExecutionID == nil ||
			*item.ParentExecutionID != target.ID) {
			continue
		}
		require.Eventually(t, func() bool {
			engine := base.runtime.nativeWorkflowRunner().engine
			engine.mutex.Lock()
			defer engine.mutex.Unlock()

			return len(engine.active[item.ID]) == 0
		}, 5*time.Second, 10*time.Millisecond)
	}
	repeat, err := base.runtime.CancelSessionAgentRun(t.Context(),
		target.SessionID, *target.AgentRunID)
	require.NoError(t, err)
	assert.False(t, repeat.CancelRequested)
}

func assertCancelledNativeTree(
	t *testing.T,
	base runtimeFixture,
	target *models.Execution,
	items []*models.Execution,
	waits map[uuid.UUID]*models.ExecutionActivity,
) {
	t.Helper()
	count := 0
	for _, item := range items {
		stored, err := base.store.GetExecution(t.Context(), item.SessionID,
			item.ID)
		require.NoError(t, err)
		if item.ID != target.ID && (item.ParentExecutionID == nil ||
			*item.ParentExecutionID != target.ID) {
			assert.Equal(t, item.State, stored.State)

			continue
		}
		count++
		assert.Equal(t, models.ExecutionStateCancelled, stored.State)
		activity, err := base.store.GetExecutionActivity(t.Context(),
			item.SessionID, waits[item.ID].ID)
		require.NoError(t, err)
		assert.Equal(t, models.ActivityStateCancelled, activity.State)
		assert.Equal(t, waits[item.ID].InputJSON, activity.InputJSON)
		registry := base.runtime.existingAgentRuns(item.SessionID)
		run, found := registry.Get(*item.AgentRunID)
		require.True(t, found)
		assert.Equal(t, AgentRunStateCancelled, run.Snapshot().State)
		select {
		case <-run.done:
		default:
			t.Fatal("cancelled child completion signal remains open")
		}
		instanceID := fmt.Sprintf("%s/%s", item.SessionID, item.ID)
		instance := &workflow.Instance{
			InstanceID: instanceID,
			ExecutionID: uuid.NewSHA1(uuid.NameSpaceOID,
				[]byte(instanceID)).String(),
		}
		require.Eventually(t, func() bool {
			state, err := base.runtime.nativeWorkflowRunner().api.
				GetWorkflowInstanceState(t.Context(), instance)

			return err == nil && state == core.WorkflowInstanceStateFinished
		}, 10*time.Second, 10*time.Millisecond)
	}
	assert.Equal(t, 4, count)
}

func TestNativeChildCancellationRestoredOnWorkerStart(t *testing.T) {
	t.Parallel()
	fixture, input, sink := childModelFixture(t, elelemtest.Text("must not execute"))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	remote, err := fixture.storage.WorkerBackend(ctx, uuid.New())
	require.NoError(t, err)
	api := newWorkflowClient(remote)
	input.Execution.MaxConcurrentTools = 1
	instance, err := api.CreateWorkflowInstance(ctx,
		client.WorkflowInstanceOptions{
			InstanceID: fmt.Sprintf("%s/%s", input.Execution.SessionID,
				input.Execution.ExecutionID),
			Queue: workflow.Queue(input.Execution.SessionID.String()),
		}, agentWorkflowName, input.Execution)
	require.NoError(t, err)
	_, requested, err := fixture.base.store.RequestAgentRunCancellation(ctx,
		input.Execution.SessionID, sink.run.ID)
	require.NoError(t, err)
	assert.True(t, requested)
	assembled, err := NewWorkflowWorker(ctx, fixture.base.runtime, remote,
		uuid.New(), input.Execution.SessionID, nil)
	require.NoError(t, err)
	require.NoError(t, assembled.Start(ctx))
	t.Cleanup(func() {
		cancel()
		require.NoError(t, assembled.WaitForCompletion())
	})
	require.Eventually(t, func() bool {
		state, err := api.GetWorkflowInstanceState(ctx, instance)

		return err == nil && state == core.WorkflowInstanceStateFinished
	}, 10*time.Second, 10*time.Millisecond)
	records, err := fixture.storage.GetWorkflowInstanceHistory(ctx,
		instance, nil)
	require.NoError(t, err)
	hasCancellation := false
	for _, record := range records {
		if record.Type == history.EventType_WorkflowExecutionCanceled {
			hasCancellation = true
		}
	}
	assert.True(t, hasCancellation)
	runs, err := fixture.base.store.ListModelRuns(ctx,
		input.Execution.SessionID, session.ListModelRunsOptions{})
	require.NoError(t, err)
	assert.Empty(t, runs.Items)
}

func TestExecutionWorkflowPauseCancelsModelAndRequiresResume(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Text("interrupted response"), elelemtest.Text("resumed response"),
	)
	fixture := newExecutionLoopFixture(t, driver, true)
	started := make(chan struct{})
	var startedOnce sync.Once
	fixture.engine.options.ModelRound = func(
		ctx context.Context, input workflowRoundInput,
	) (workflowRoundResult, error) {
		request, conversation, err := workflowModelRequest(
			fixture.base.runtime.models, input)
		if err != nil {
			return workflowRoundResult{}, err
		}

		request.OnDelta(func(ctx context.Context, _ elelem.Delta) error {
			isFirst := false
			startedOnce.Do(func() { close(started); isFirst = true })
			if isFirst {
				<-ctx.Done()

				return ctxerrors.Wrap(ctx.Err(), "interrupted test provider")
			}

			return nil
		})

		return workflowRunModelRequest(ctx, request, conversation)
	}
	instance := fixture.start(t)
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("model activity did not start")
	}
	require.NoError(t, fixture.engine.pause(t.Context(), fixture.input.SessionID,
		fixture.input.ExecutionID, "leave this task paused"))
	assert.Eventually(t, func() bool {
		records, err := fixture.storage.GetWorkflowInstanceHistory(
			t.Context(), instance, nil)
		if err != nil {
			return false
		}

		for _, event := range records {
			if event.Type == history.EventType_ActivityCompleted {
				return true
			}
		}

		return false
	}, 10*time.Second, 10*time.Millisecond)
	paused, err := fixture.base.store.GetExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, paused.State)
	require.NoError(t, fixture.api.SignalWorkflow(t.Context(),
		fmt.Sprintf("%s/%s", fixture.input.SessionID, fixture.input.ExecutionID),
		workflowResumeSignal, struct{}{}))
	require.Eventually(t, func() bool {
		records, err := fixture.storage.GetWorkflowInstanceHistory(
			t.Context(), instance, nil)
		if err != nil {
			return false
		}

		completed := 0
		for _, event := range records {
			if event.Type == history.EventType_ActivityCompleted {
				completed++
			}
		}

		return completed >= 3
	}, 10*time.Second, 10*time.Millisecond)
	assert.Equal(t, 1, driver.Calls())
	_, err = fixture.base.store.ResumeExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	require.NoError(t, fixture.api.SignalWorkflow(t.Context(),
		fmt.Sprintf("%s/%s", fixture.input.SessionID, fixture.input.ExecutionID),
		workflowResumeSignal, struct{}{}))
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "resumed response", result.Text)
	requests := driver.Requests()
	require.Len(t, requests, 2)
	assert.Equal(t, requests[0].Messages, requests[1].Messages)
	fixture.stop()
}
