package agent

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
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
