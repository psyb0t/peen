package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResumeExecutionToolRequiresActiveWorkflowCaller(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		native bool
	}{
		{name: "embedding has no workflow runner"},
		{name: "native caller has no activity", native: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			driver := elelemtest.NewScriptedDriver()
			base := newRuntimeFixture(t, driver)
			if tc.native {
				base = newNativeRunnerFixture(t, driver)
			}
			deps := &launchAgentDeps{runtime: base.runtime, sessionID: base.runtime.sessionID}
			arguments, err := json.Marshal(resumeExecutionInput{ExecutionID: uuid.New(), ControlRevision: 1})
			require.NoError(t, err)
			found := false
			for _, tool := range messageTools(deps, nil) {
				if tool.Name != toolNameResumeExecution {
					continue
				}
				found = true
				require.True(t, json.Valid(tool.ArgumentsSchema))
				result, err := tool.Handler(t.Context(), elelem.ToolInput{CallID: "resumeWork", Arguments: arguments})
				require.NoError(t, err)
				assert.True(t, result.IsError)
			}
			require.True(t, found)
			assert.Zero(t, driver.Calls())
			page, err := base.store.SearchExecutions(t.Context(), base.runtime.sessionID, session.SearchExecutionsOptions{})
			require.NoError(t, err)
			assert.Empty(t, page.Items)
		})
	}
}

func TestResumeExecutionToolContinuesSelectedStoppedDeferredInput(t *testing.T) {
	t.Parallel()
	fixture, message, dispatch := deferredRootFixture(t)
	_, err := fixture.base.store.Cancel(t.Context(), message.SessionID)
	require.NoError(t, err)
	arguments := fmt.Sprintf(`{"executionId":%q,"controlRevision":0,"deferredRequestId":%q,"deferredStopRevision":0}`, message.ExecutionID, message.RequestID)
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("continueDeferred", toolNameResumeExecution, arguments),
		elelemtest.Text("current request finished"), elelemtest.Text("deferred input finished"))
	registry, err := NewStaticRegistry(map[string]ModelClient{
		runtimeTestModelReference: {Client: elelem.New(driver), Model: elelem.Model{ID: runtimeTestModelID}},
	})
	require.NoError(t, err)
	fixture.base.runtime.models = registry
	native := &nativeWorkflowRunner{api: newWorkflowClient(fixture.storage), engine: fixture.engine, dispatch: dispatch}
	fixture.base.runtime.workflowMutex.Lock()
	fixture.base.runtime.workflowRunner = native
	fixture.base.runtime.workflowMutex.Unlock()
	current, err := fixture.base.runtime.Run(t.Context(), TurnRequest{Message: "continue the selected stopped input"})
	require.NoError(t, err)
	assert.Equal(t, "current request finished", current.Text)
	row, err := fixture.base.store.GetExecutionMessageByRequest(t.Context(), message.SessionID, message.ExecutionID, message.RequestID)
	require.NoError(t, err)
	require.Equal(t, models.InboxMessageStateForwarded, row.State)
	require.NotNil(t, row.ForwardedExecutionID)
	require.Eventually(t, func() bool {
		saved, err := fixture.base.store.GetExecution(t.Context(), message.SessionID, *row.ForwardedExecutionID)

		return err == nil && saved.State == models.ExecutionStateCompleted
	}, workflowTestResultTimeout, 10*time.Millisecond)
	turn, err := fixture.base.store.GetExecutionRootTurn(t.Context(), message.SessionID, *row.ForwardedExecutionID)
	require.NoError(t, err)
	assert.Equal(t, message.RequestID, turn.RequestID)
	assert.EqualValues(t, 1, turn.StopRevision)
	assert.EqualValues(t, 3, driver.Calls())
	source, err := fixture.base.store.GetExecution(t.Context(), message.SessionID, message.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateCompleted, source.State)
}

// Work stopped mid-call keeps that call with an unknown effect. Resuming it
// without resolving the call first used to leave the work in reconciliation
// for good while the agent believed it was running. The resumed work now
// reads the call as an error result saying its outcome is unknown, and the
// call is not repeated.
func TestResumeExecutionToolSettlesTheCallAStopInterrupted(t *testing.T) {
	t.Parallel()
	initial := elelemtest.NewScriptedDriver(elelemtest.ToolCall("writeUnknownEffect", nativeRunnerBlockingTool, `{}`))
	driver := &inspectionControlDriver{ScriptedDriver: initial}
	base := newNativeRunnerFixture(t, driver)
	effectPath := filepath.Join(base.workspace, "resumed-effect.txt")
	effectStarted := make(chan struct{})
	var effects atomic.Int64
	base.runtime.extraTools = []elelem.Tool{{
		Name: nativeRunnerBlockingTool, ArgumentsSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, _ elelem.ToolInput) (elelem.ToolResult, error) {
			effects.Add(1)
			if err := os.WriteFile(effectPath, []byte("written once"), runtimeTestFileMode); err != nil {
				return elelem.ToolResult{}, ctxerrors.Wrap(err, "write resume fixture effect")
			}
			close(effectStarted)
			<-ctx.Done()

			return elelem.ToolResult{}, ctxerrors.Wrap(ctx.Err(), "interrupt unacknowledged effect")
		},
	}}
	first := runHandoffTurnAsync(t.Context(), base.runtime, "perform the original file effect")
	select {
	case <-effectStarted:
	case <-time.After(workflowTestResultTimeout):
		t.Fatal("original effect did not start")
	}
	_, err := base.runtime.CancelSession(t.Context(), base.runtime.sessionID)
	require.NoError(t, err)
	select {
	case err := <-first:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(workflowTestResultTimeout):
		t.Fatal("original effect did not stop")
	}
	page, err := base.store.SearchExecutions(t.Context(), base.runtime.sessionID, session.SearchExecutionsOptions{State: models.ExecutionStatePaused})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	original := page.Items[0]
	require.Eventually(t, func() bool {
		activities, err := base.store.ListExecutionActivities(t.Context(), original.SessionID, original.ID, session.ListEventsOptions{})
		if err != nil {
			return false
		}
		for _, activity := range activities.Items {
			if activity.State == models.ActivityStateUncertain {
				return true
			}
		}

		return false
	}, workflowTestResultTimeout, 10*time.Millisecond)
	resume, err := json.Marshal(resumeExecutionInput{ExecutionID: original.ID, ControlRevision: original.ControlRevision})
	require.NoError(t, err)
	driver.ScriptedDriver = elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("resumeStopped", toolNameResumeExecution, string(resume)),
		elelemtest.Text("resume request completed"), elelemtest.Text("original work finished"))
	_, err = base.runtime.Run(t.Context(), TurnRequest{Message: "continue the stopped work"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		stored, err := base.store.GetExecution(t.Context(), original.SessionID, original.ID)

		return err == nil && stored.State == models.ExecutionStateCompleted
	}, workflowTestResultTimeout, 10*time.Millisecond)
	assert.EqualValues(t, 1, effects.Load(), "the interrupted call is not repeated")
	content, err := os.ReadFile(effectPath)
	require.NoError(t, err)
	assert.Equal(t, "written once", string(content))
	requests := driver.Requests()
	require.Len(t, requests, 3)
	toolResults := 0
	for _, message := range requests[2].Messages {
		if message.Role != elelem.RoleTool || message.ToolCallID != "writeUnknownEffect" {
			continue
		}
		toolResults++
		assert.Equal(t, fmt.Sprintf(interruptedToolResultFormat, original.PauseReason),
			message.Text())
	}
	assert.Equal(t, 1, toolResults)
	resumeResults := 0
	for _, message := range requests[1].Messages {
		if message.Role != elelem.RoleTool || message.ToolCallID != "resumeStopped" {
			continue
		}
		resumeResults++
		assert.Contains(t, message.Text(), resumedRootNote,
			"resumed root work tells the agent not to wait for it")
	}
	assert.Equal(t, 1, resumeResults)
}

// A model that lost track of the real executionId can send one the session
// does not have. It used to read the store's internal RPC error. It now
// reads that the session has no such execution and how to find the real one.
func TestResumeExecutionToolNamesAnUnknownExecution(t *testing.T) {
	t.Parallel()
	unknown := uuid.New()
	arguments, err := json.Marshal(resumeExecutionInput{ExecutionID: unknown, ControlRevision: 0})
	require.NoError(t, err)
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("resumeUnknown", toolNameResumeExecution, string(arguments)),
		elelemtest.Text("nothing to resume"))
	base := newNativeRunnerFixture(t, driver)
	_, err = base.runtime.Run(t.Context(), TurnRequest{Message: "continue the crashed work"})
	require.NoError(t, err)
	requests := driver.Requests()
	require.Len(t, requests, 2)
	results := 0
	for _, message := range requests[1].Messages {
		if message.Role != elelem.RoleTool || message.ToolCallID != "resumeUnknown" {
			continue
		}
		results++
		assert.True(t, message.ToolResultIsError)
		assert.Contains(t, message.Text(), fmt.Sprintf(resumeExecutionNotFoundFormat, unknown))
		assert.NotContains(t, message.Text(), "execution.store")
	}
	assert.Equal(t, 1, results)
}
