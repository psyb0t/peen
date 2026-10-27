package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/go-workflows/backend"
	"github.com/psyb0t/go-workflows/client"
	"github.com/psyb0t/go-workflows/workflow"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rootHandoffDriver struct {
	*elelemtest.ScriptedDriver
	calls           atomic.Int64
	originalStarted chan struct{}
	currentStarted  chan struct{}
	finishCurrent   chan struct{}
}

func (d *rootHandoffDriver) Stream(ctx context.Context, request elelem.DriverRequest, onDelta func(elelem.Delta) error) (elelem.Usage, error) {
	switch d.calls.Add(1) {
	case 1:
		close(d.originalStarted)
		<-ctx.Done()

		return elelem.Usage{}, ctxerrors.Wrap(ctx.Err(), "interrupt original request")
	case 3:
		close(d.currentStarted)
		select {
		case <-d.finishCurrent:
		case <-ctx.Done():
			return elelem.Usage{}, ctxerrors.Wrap(ctx.Err(), "interrupt current request")
		}
	}

	return d.ScriptedDriver.Stream(ctx, request, onDelta)
}

func TestWorkflowRootHandoffContinuesAfterCurrentTurn(t *testing.T) {
	t.Parallel()
	driver := &rootHandoffDriver{
		ScriptedDriver:  elelemtest.NewScriptedDriver(),
		originalStarted: make(chan struct{}), currentStarted: make(chan struct{}), finishCurrent: make(chan struct{}),
	}
	base := newNativeRunnerFixture(t, driver)
	first := runHandoffTurnAsync(t.Context(), base.runtime, "continue original work")
	select {
	case <-driver.originalStarted:
	case <-time.After(workflowTestResultTimeout):
		t.Fatal("original model did not start")
	}
	_, err := base.runtime.CancelSession(t.Context(), base.runtime.sessionID)
	require.NoError(t, err)
	select {
	case err := <-first:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(workflowTestResultTimeout):
		t.Fatal("original turn did not pause")
	}
	page, err := base.store.SearchExecutions(t.Context(), base.runtime.sessionID, session.SearchExecutionsOptions{State: models.ExecutionStatePaused})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	original := page.Items[0]
	arguments, err := json.Marshal(resumeExecutionInput{ExecutionID: original.ID, ControlRevision: original.ControlRevision})
	require.NoError(t, err)
	driver.ScriptedDriver = elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("resumeOriginal", toolNameResumeExecution, string(arguments)),
		elelemtest.Text("current result"), elelemtest.Text("original result"))
	second := runHandoffTurnAsync(t.Context(), base.runtime, "perform current work")
	select {
	case <-driver.currentStarted:
	case <-time.After(workflowTestResultTimeout):
		t.Fatal("current model did not start")
	}
	require.Eventually(t, func() bool {
		activities, err := base.store.ListExecutionActivities(t.Context(), original.SessionID, original.ID, session.ListEventsOptions{})
		if err != nil {
			return false
		}
		for _, activity := range activities.Items {
			if activity.Kind == models.ActivityKindModel && activity.Attempt >= 2 && activity.State == models.ActivityStateFailed {
				return true
			}
		}

		return false
	}, workflowTestResultTimeout, 10*time.Millisecond)
	assert.EqualValues(t, 3, driver.calls.Load())
	close(driver.finishCurrent)
	select {
	case err := <-second:
		require.NoError(t, err)
	case <-time.After(workflowTestResultTimeout):
		t.Fatal("current turn did not finish")
	}
	require.Eventually(t, func() bool {
		stored, err := base.store.GetExecution(t.Context(), original.SessionID, original.ID)

		return err == nil && stored.State == models.ExecutionStateCompleted
	}, workflowTestResultTimeout, 10*time.Millisecond)
	turn, err := base.store.GetExecutionRootTurn(t.Context(), original.SessionID, original.ID)
	require.NoError(t, err)
	assert.Equal(t, original.RootTurnID, turn.ID)
	assert.Equal(t, models.TurnStateCompleted, turn.State)
	assert.False(t, turn.CancelRequested)
	assert.EqualValues(t, 4, driver.calls.Load())
	requests := driver.Requests()
	require.Len(t, requests, 3)
	// The current turn reads the stopped turn in its history, ending with
	// the system message Peen added when it stopped, then its own message.
	opening := requests[0].Messages
	require.GreaterOrEqual(t, len(opening), 3)
	assert.Equal(t, "perform current work", opening[len(opening)-1].Text())
	stopped := opening[len(opening)-2]
	assert.Equal(t, elelem.RoleSystem, stopped.Role)
	assert.Contains(t, stopped.Text(), "The user stopped it.")
	assert.Equal(t, "continue original work", opening[len(opening)-3].Text())
	assert.Equal(t, "continue original work", requests[2].Messages[len(requests[2].Messages)-1].Text())
}

func runHandoffTurnAsync(ctx context.Context, runtime *Runtime, message string) <-chan error {
	finished := make(chan error, 1)
	go func() {
		defer func() {
			if recover() != nil {
				finished <- ctxerrors.New("handoff test turn panicked")
			}
		}()
		_, err := runtime.Run(ctx, TurnRequest{Message: message})
		finished <- err
	}()

	return finished
}

func TestWorkflowHandoffRecoversBeforeSDKSubmission(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		paused bool
	}{
		{name: "saved before submission"}, {name: "stopped before submission", paused: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, prepared, dispatch := newRootSubmissionFixture(t)
			dispatch.runtime.maxToolRounds = 2
			dispatch.runtime.maxConcurrentTools = 3
			input, err := dispatch.prepareRootSubmission(t.Context(), prepared)
			require.NoError(t, err)
			native := &nativeWorkflowRunner{api: newWorkflowClient(fixture.storage), engine: fixture.engine, dispatch: dispatch}
			if tc.paused {
				_, err = fixture.base.store.PauseExecution(t.Context(), input.SessionID, input.ExecutionID, "retain unsubmitted work")
				require.NoError(t, err)
				require.NoError(t, native.restoreWorkflowControls(t.Context()))
				stats, err := fixture.storage.GetStats(t.Context())
				require.NoError(t, err)
				assert.Zero(t, stats.ActiveWorkflowInstances)
				_, err = fixture.base.store.ResumeExecution(t.Context(), input.SessionID, input.ExecutionID)
				require.NoError(t, err)
			}
			dispatch.runtime.maxToolRounds = 1
			dispatch.runtime.maxConcurrentTools = 1
			require.NoError(t, native.restoreWorkflowControls(t.Context()))
			instanceID := fmt.Sprintf("%s/%s", input.SessionID, input.ExecutionID)
			instance := &workflow.Instance{InstanceID: instanceID, ExecutionID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(instanceID)).String()}
			_, err = native.api.GetWorkflowInstanceState(t.Context(), instance)
			require.NoError(t, err)
			result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(), native.api, instance, workflowTestResultTimeout)
			require.NoError(t, err)
			assert.Equal(t, "submitted result", result.Text)
			stored, err := fixture.base.store.GetExecution(t.Context(), input.SessionID, input.ExecutionID)
			require.NoError(t, err)
			assert.Equal(t, instance.ExecutionID, stored.WorkflowExecutionID)
			assert.Equal(t, models.ExecutionStateCompleted, stored.State)
			turn, err := fixture.base.store.GetExecutionRootTurn(t.Context(), input.SessionID, input.ExecutionID)
			require.NoError(t, err)
			assert.Equal(t, prepared.lease.TurnID, turn.ID)
			history, err := fixture.storage.GetWorkflowInstanceHistory(t.Context(), instance, nil)
			require.NoError(t, err)
			require.NoError(t, native.restoreWorkflowControls(t.Context()))
			after, err := fixture.storage.GetWorkflowInstanceHistory(t.Context(), instance, nil)
			require.NoError(t, err)
			assert.Equal(t, history, after)
		})
	}
}

func TestWorkflowHandoffSchedulesRetainedDeferredInput(t *testing.T) {
	t.Parallel()
	fixture, message, dispatch := deferredRootFixture(t)
	native := &nativeWorkflowRunner{api: newWorkflowClient(fixture.storage), engine: fixture.engine, dispatch: dispatch}
	require.NoError(t, native.restoreWorkflowControls(t.Context()))
	row, err := fixture.base.store.GetExecutionMessageByRequest(t.Context(), message.SessionID, message.ExecutionID, message.RequestID)
	require.NoError(t, err)
	require.Equal(t, models.InboxMessageStateForwarded, row.State)
	require.NotNil(t, row.ForwardedExecutionID)
	instance := nativeWorkflowInstance(message.SessionID, *row.ForwardedExecutionID)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(), native.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, "submitted result", result.Text)
	saved, err := fixture.base.store.GetExecution(t.Context(), message.SessionID, *row.ForwardedExecutionID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateCompleted, saved.State)
	turn, err := fixture.base.store.GetExecutionRootTurn(t.Context(), saved.SessionID, saved.ID)
	require.NoError(t, err)
	assert.Equal(t, message.RequestID, turn.RequestID)
	require.NoError(t, native.restoreWorkflowControls(t.Context()))
	page, err := fixture.base.store.SearchExecutions(t.Context(), message.SessionID, session.SearchExecutionsOptions{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 2)
}

func TestWorkflowHandoffLeavesUnboundLiveRootForItsCaller(t *testing.T) {
	t.Parallel()
	fixture, _ := newRootModelWorkflowFixture(t, elelemtest.NewScriptedDriver())
	instance := nativeWorkflowInstance(fixture.input.SessionID, fixture.input.ExecutionID)
	repo := repositories.Use(fixture.base.handle.GormDB).Execution
	_, err := repo.WithContext(t.Context()).Where(repo.ID.Eq(fixture.input.ExecutionID)).UpdateSimple(repo.WorkflowExecutionID.Value(instance.ExecutionID))
	require.NoError(t, err)
	execution, err := fixture.base.store.GetExecution(t.Context(), fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	native := &nativeWorkflowRunner{
		api: newWorkflowClient(fixture.storage), engine: fixture.engine,
		dispatch: newWorkflowDispatch(t.Context(), fixture.base.runtime, fixture.engine.options.GenerationID, nil),
	}
	require.True(t, fixture.base.store.IsActive(fixture.input.SessionID))
	require.NoError(t, native.nudgeExecution(t.Context(), execution))
	_, err = native.api.GetWorkflowInstanceState(t.Context(), instance)
	require.ErrorIs(t, err, backend.ErrInstanceNotFound)
}
