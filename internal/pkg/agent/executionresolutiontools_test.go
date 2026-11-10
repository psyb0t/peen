package agent

import (
	"context"
	"encoding/json"
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

type inspectionControlDriver struct {
	*elelemtest.ScriptedDriver
}

func TestAgentInspectionToolRequiresCurrentWorkflowCaller(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		native bool
	}{
		{name: "unsupported embedded loop"},
		{name: "missing live tool lease", native: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			driver := elelemtest.NewScriptedDriver()
			var base runtimeFixture
			if tc.native {
				base = newNativeRunnerFixture(t, driver)
			} else {
				base = newRuntimeFixture(t, driver)
			}
			deps := &launchAgentDeps{runtime: base.runtime, sessionID: base.runtime.sessionID}
			input, err := json.Marshal(resolveExecutionActivityInput{
				ActivityID: uuid.New(), ControlRevision: 1,
				ResolveActivityInput: session.ResolveActivityInput{Attempt: 1, Resolution: session.ActivityResolutionRetry, Evidence: "inspected the saved step"},
			})
			require.NoError(t, err)
			found := false
			for _, tool := range messageTools(deps, nil) {
				if tool.Name != toolNameResolveExecutionActivity {
					continue
				}
				found = true
				result, err := tool.Handler(t.Context(), elelem.ToolInput{CallID: "resolveUnknown", Arguments: input})
				require.NoError(t, err)
				assert.True(t, result.IsError)
			}
			require.True(t, found)
			assert.Zero(t, driver.Calls())
		})
	}
}

const inspectedEffectResult = "the inspected file effect completed"

// acceptedOutput builds what the agent sends to accept the uncertain tool
// call: an output and a context, from the step input it read.
type acceptedOutput func(t *testing.T, retained workflowToolPhaseInput) (
	json.RawMessage, json.RawMessage)

func TestAgentInspectionAcceptsWrittenEffectWithoutRepeatingIt(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		accept acceptedOutput
	}{
		{
			name: "whole phase state",
			accept: func(t *testing.T, retained workflowToolPhaseInput) (
				json.RawMessage, json.RawMessage,
			) {
				t.Helper()
				accepted := retained.State
				accepted.LastPhase = workflowToolPhaseHandler
				accepted.RootProjection, accepted.ChildProjection = nil, nil
				accepted.Outcome.Result = elelem.ToolResult{Content: inspectedEffectResult}
				accepted.Outcome.Context = retained.Tool.Round.Execution.Context
				output, err := json.Marshal(accepted)
				require.NoError(t, err)

				return output, accepted.Outcome.Context
			},
		},
		{
			// The agent cannot know Peen's phase state, so the tool result
			// alone, with no context, has to be enough.
			name: "tool result only",
			accept: func(t *testing.T, _ workflowToolPhaseInput) (
				json.RawMessage, json.RawMessage,
			) {
				t.Helper()
				output, err := json.Marshal(elelem.ToolResult{Content: inspectedEffectResult})
				require.NoError(t, err)

				return output, nil
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			acceptInspectedEffect(t, tc.accept)
		})
	}
}

func TestAgentInspectionRefusesAcceptWithoutToolResult(t *testing.T) {
	t.Parallel()
	_, err := acceptedToolPhaseOutput(`{"phase":"handler"}`,
		json.RawMessage(`{"lastPhase":"handler"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the tool's result")
}

func TestAgentInspectionRetryDropsAWrittenOutcome(t *testing.T) {
	t.Parallel()
	deps := &launchAgentDeps{}
	input := resolveExecutionActivityInput{
		ResolveActivityInput: session.ResolveActivityInput{
			Attempt: 1, Resolution: session.ActivityResolutionRetry,
			Evidence: "the step left no effect",
			Output:   json.RawMessage(`{"Content":"not run yet"}`),
			Context:  json.RawMessage(`{}`),
		},
	}

	require.NoError(t, deps.prepareResolution(t.Context(), &input))
	assert.Nil(t, input.Output)
	assert.Nil(t, input.Context)
	assert.Equal(t, session.ActivityResolutionRetry, input.Resolution)
	assert.Equal(t, "the step left no effect", input.Evidence)
}

func acceptInspectedEffect(t *testing.T, accept acceptedOutput) {
	t.Helper()
	initial := elelemtest.NewScriptedDriver(elelemtest.ToolCall("writeUnknownEffect", nativeRunnerBlockingTool, `{}`))
	driver := &inspectionControlDriver{ScriptedDriver: initial}
	base := newNativeRunnerFixture(t, driver)
	effectPath := filepath.Join(base.workspace, "inspected-effect.txt")
	effectStarted := make(chan struct{})
	var effects atomic.Int64
	base.runtime.extraTools = []elelem.Tool{{
		Name: nativeRunnerBlockingTool, ArgumentsSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, _ elelem.ToolInput) (elelem.ToolResult, error) {
			effects.Add(1)
			if err := os.WriteFile(effectPath, []byte("written once"), runtimeTestFileMode); err != nil {
				return elelem.ToolResult{}, ctxerrors.Wrap(err, "write inspection fixture effect")
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
	var unknown *models.ExecutionActivity
	var retained workflowToolPhaseInput
	require.Eventually(t, func() bool {
		activities, err := base.store.ListExecutionActivities(t.Context(), original.SessionID, original.ID, session.ListEventsOptions{})
		if err != nil {
			return false
		}
		for _, activity := range activities.Items {
			if activity.State != models.ActivityStateUncertain || json.Unmarshal([]byte(activity.InputJSON), &retained) != nil || retained.Phase != workflowToolPhaseHandler {
				continue
			}
			unknown = activity

			return true
		}

		return false
	}, workflowTestResultTimeout, 10*time.Millisecond)
	content, err := os.ReadFile(effectPath)
	require.NoError(t, err)
	require.Equal(t, "written once", string(content))
	output, acceptedContext := accept(t, retained)
	resolution, err := json.Marshal(resolveExecutionActivityInput{
		ActivityID: unknown.ID, ControlRevision: original.ControlRevision,
		ResolveActivityInput: session.ResolveActivityInput{
			Attempt: unknown.Attempt, Resolution: session.ActivityResolutionAccept,
			Evidence: "read the effect file and confirmed its complete content",
			Output:   output, Context: acceptedContext,
		},
	})
	require.NoError(t, err)
	resume, err := json.Marshal(resumeExecutionInput{ExecutionID: original.ID, ControlRevision: original.ControlRevision})
	require.NoError(t, err)
	driver.ScriptedDriver = elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("inspectOriginal", toolNameResolveExecutionActivity, string(resolution)),
		elelemtest.ToolCall("resumeInspected", toolNameResumeExecution, string(resume)),
		elelemtest.Text("inspection request completed"), elelemtest.Text("original effect retained"))
	_, err = base.runtime.Run(t.Context(), TurnRequest{Message: "inspect and continue the original effect"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		stored, err := base.store.GetExecution(t.Context(), original.SessionID, original.ID)

		return err == nil && stored.State == models.ExecutionStateCompleted
	}, workflowTestResultTimeout, 10*time.Millisecond)
	assert.EqualValues(t, 1, effects.Load())
	assert.Equal(t, 1, initial.Calls())
	assert.Equal(t, 4, driver.Calls())
	stored, err := base.store.GetExecutionActivity(t.Context(), original.SessionID, unknown.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ActivityStateCompleted, stored.State)
	assert.Equal(t, unknown.Attempt, stored.Attempt)
	turn, err := base.store.GetExecutionRootTurn(t.Context(), original.SessionID, original.ID)
	require.NoError(t, err)
	assert.Equal(t, original.RootTurnID, turn.ID)
	content, err = os.ReadFile(effectPath)
	require.NoError(t, err)
	assert.Equal(t, "written once", string(content))
	requests := driver.Requests()
	require.Len(t, requests, 4)
	toolResults := 0
	for _, message := range requests[3].Messages {
		if message.Role == elelem.RoleTool && message.ToolCallID == "writeUnknownEffect" {
			assert.Equal(t, inspectedEffectResult,
				toolOutputText(t, message.Text()))
			toolResults++
		}
	}
	assert.Equal(t, 1, toolResults)
}
