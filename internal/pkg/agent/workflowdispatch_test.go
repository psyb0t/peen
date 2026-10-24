package agent

import (
	"encoding/json"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/go-workflows/client"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Workflow completion includes SQLite checkpoints under race and coverage
// instrumentation.
const workflowTestResultTimeout = time.Minute

func TestWorkflowDispatchRunsRootThroughOwnedCallbacks(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("dispatchRootRead", toolNameReadFile,
			`{"path":"dispatch.txt"}`), elelemtest.Text("native root result"))
	fixture, prepared := newRootModelWorkflowFixture(t, driver)
	writeRuntimeFile(t, filepath.Join(fixture.base.workspace, "dispatch.txt"),
		"root dispatch source")
	dispatcher := newWorkflowDispatch(t.Context(), fixture.base.runtime,
		fixture.engine.options.GenerationID, nil)
	dispatcher.bind(&fixture.engine.options)
	instance := startCompactingWorkflow(t, fixture)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, "native root result", result.Text)
	execution, err := fixture.base.store.GetExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateCompleted, execution.State)
	turn, err := fixture.base.store.GetExecutionRootTurn(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, prepared.lease.TurnID, turn.ID)
	assert.Equal(t, models.TurnStateCompleted, turn.State)
	assert.Equal(t, 2, driver.Calls())
	fixture.stop()
}

func TestWorkflowDispatchRunsChildThroughOwnedCallbacks(t *testing.T) {
	t.Parallel()
	fixture, input, original := childModelFixture(t,
		elelemtest.ToolCall("dispatchChildRead", toolNameReadFile,
			`{"path":"dispatch.txt"}`), elelemtest.Text("native child result"))
	writeRuntimeFile(t, filepath.Join(fixture.base.workspace, "dispatch.txt"),
		"child dispatch source")
	var terminal atomic.Int64
	dispatcher := newWorkflowDispatch(t.Context(), fixture.base.runtime,
		fixture.engine.options.GenerationID, func(event Event) error {
			if event.Type == EventTypeAgentRunCompleted {
				terminal.Add(1)
			}

			return nil
		})
	dispatcher.bind(&fixture.engine.options)
	fixture.input = input.Execution
	fixture.input.MaxConcurrentTools = 1
	instance := startCompactingWorkflow(t, fixture)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, "native child result", result.Text)
	registry, err := fixture.base.runtime.sessionAgentRuns(input.Execution.SessionID)
	require.NoError(t, err)
	run, found := registry.Get(original.run.ID)
	require.True(t, found)
	assert.Equal(t, AgentRunStateCompleted, run.Snapshot().State)
	assert.Equal(t, int64(1), terminal.Load())
	fixture.stop()
}

func TestWorkflowDispatchUsesStoredKindAndRefusesForeignExecution(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childModelFixture(t, elelemtest.Text("must not run"))
	dispatcher := newWorkflowDispatch(t.Context(), fixture.base.runtime,
		fixture.engine.options.GenerationID, nil)
	before, err := fixture.base.store.GetExecution(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID)
	require.NoError(t, err)
	input.Execution.Context = json.RawMessage("malformed context")
	callbacks, err := dispatcher.callbacks(t.Context(), input.Execution)
	require.NoError(t, err)
	assert.Same(t, &dispatcher.childOptions, callbacks)
	_, err = dispatcher.modelRound(t.Context(), input)
	var syntax *json.SyntaxError
	require.ErrorAs(t, err, &syntax)
	after, err := fixture.base.store.GetExecution(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	audits, err := fixture.base.store.ListModelRuns(t.Context(),
		input.Execution.SessionID, session.ListModelRunsOptions{})
	require.NoError(t, err)
	assert.Empty(t, audits.Items)
	input.Execution.SessionID = uuid.New()
	_, err = dispatcher.callbacks(t.Context(), input.Execution)
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

func TestWorkflowDispatchRejectsMissingStorageForEveryPhase(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name string
		run  func(*workflowDispatch) error
	}{
		{name: "prepare", run: func(d *workflowDispatch) error {
			_, err := d.prepareRound(t.Context(), workflowRoundInput{})

			return err
		}},
		{name: "compact", run: func(d *workflowDispatch) error {
			_, err := d.compactRound(t.Context(), workflowRoundInput{})

			return err
		}},
		{name: "model", run: func(d *workflowDispatch) error {
			_, err := d.modelRound(t.Context(), workflowRoundInput{})

			return err
		}},
		{name: "tool", run: func(d *workflowDispatch) error {
			_, err := d.toolCall(t.Context(), workflowToolInput{})

			return err
		}},
		{name: "before finish", run: func(d *workflowDispatch) error {
			_, err := d.beforeFinish(t.Context(), workflowFinishInput{})

			return err
		}},
		{name: "after finish", run: func(d *workflowDispatch) error {
			return d.afterFinish(t.Context(), workflowFinishInput{}, workflowFinishResult{})
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dispatcher := newWorkflowDispatch(t.Context(), &Runtime{}, uuid.New(), nil)
			require.ErrorIs(t, tc.run(dispatcher), commerr.ErrRequiredFieldNotSet)
		})
	}
}
