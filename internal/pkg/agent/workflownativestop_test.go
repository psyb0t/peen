package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/cschleiden/go-workflows/client"
	"github.com/cschleiden/go-workflows/workflow"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func coldNativeStopFixture(t *testing.T) (
	executionLoopFixture, <-chan struct{}, <-chan struct{},
) {
	t.Helper()
	started, stopped := make(chan struct{}), make(chan struct{})
	tool := elelem.Tool{
		Name:            nativeRunnerBlockingTool,
		ArgumentsSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, _ elelem.ToolInput) (
			elelem.ToolResult, error,
		) {
			close(started)
			<-ctx.Done()
			close(stopped)

			return elelem.ToolResult{}, ctxerrors.Wrap(ctx.Err(),
				"interrupt cold native tool")
		},
	}
	fixture, original := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver(
			elelemtest.ToolCall("coldPending", nativeRunnerBlockingTool, `{}`),
			elelemtest.Text("unrelated work completed")), tool)
	runtime := fixture.base.runtime
	runtime.closeActiveUserMessageQueue(original)
	fixture.base.store.ReleaseTurn(original.lease)
	dispatch := newWorkflowDispatch(fixture.engine.options.Lifecycle, runtime,
		fixture.engine.options.GenerationID, nil)
	dispatch.bind(&fixture.engine.options)
	require.NoError(t, runtime.bindWorkflowRunner(fixture.storage, dispatch,
		fixture.engine, fixture.input.SessionID))
	require.NoError(t, fixture.worker.Start(fixture.engine.options.Lifecycle))
	fixture.started.Store(true)
	_, err := fixture.api.CreateWorkflowInstance(t.Context(),
		client.WorkflowInstanceOptions{
			InstanceID: fmt.Sprintf("%s/%s", fixture.input.SessionID,
				fixture.input.ExecutionID),
			Queue: workflow.Queue(fixture.input.SessionID.String()),
		}, agentWorkflowName, fixture.input)
	require.NoError(t, err)

	return fixture, started, stopped
}

func TestNativeSessionStopCancelsColdToolAndPermitsUnrelatedWork(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name             string
		controllerPaused bool
	}{
		{name: "worker pauses"},
		{name: "controller already paused", controllerPaused: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertNativeSessionStop(t, tc.controllerPaused)
		})
	}
}

func assertNativeSessionStop(t *testing.T, controllerPaused bool) {
	t.Helper()
	fixture, started, stopped := coldNativeStopFixture(t)
	select {
	case <-started:
	case <-time.After(workflowTestResultTimeout):
		t.Fatal("cold native tool did not start")
	}

	if controllerPaused {
		_, err := fixture.base.store.PauseExecution(t.Context(),
			fixture.input.SessionID, fixture.input.ExecutionID, "client left")
		require.NoError(t, err)
	}

	paused, err := fixture.base.runtime.PauseSessionWorkflows(t.Context())
	require.NoError(t, err)
	assert.True(t, paused)
	select {
	case <-stopped:
	case <-time.After(workflowTestResultTimeout):
		t.Fatal("cold native tool did not stop")
	}

	before, err := fixture.base.store.GetExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, before.State)
	assert.False(t, fixture.base.store.IsActive(fixture.input.SessionID))
	paused, err = fixture.base.runtime.PauseSessionWorkflows(t.Context())
	require.NoError(t, err)
	assert.True(t, paused)

	result, err := fixture.base.runtime.Run(t.Context(), TurnRequest{
		RequestID: uuid.New(), Message: "perform unrelated work",
	})
	require.NoError(t, err)
	assert.Equal(t, "unrelated work completed", result.Text)
	after, err := fixture.base.store.GetExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, before.State, after.State)
	assert.Equal(t, before.ControlRevision, after.ControlRevision)
}
