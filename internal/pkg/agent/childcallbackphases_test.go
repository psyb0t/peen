package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/go-workflows/client"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBoundChildCallbacksRunRegisteredWorkflowAndSaveFinalHistory(t *testing.T) {
	t.Parallel()
	fixture, input, original := childModelFixture(t,
		elelemtest.ToolCall("nativeChildRead", toolNameReadFile,
			`{"path":"child.txt"}`), elelemtest.Text("native child finished"))
	writeRuntimeFile(t, filepath.Join(fixture.base.workspace, "child.txt"),
		"child source")
	callbacks := newChildWorkflowCallbacks(t.Context(), fixture.base.runtime, nil)
	callbacks.bind(&fixture.engine.options)
	fixture.input = input.Execution
	fixture.input.MaxConcurrentTools = 2
	instance := startCompactingWorkflow(t, fixture)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, "native child finished", result.Text)
	stored, err := fixture.base.store.GetAgentRun(t.Context(),
		input.Execution.SessionID, original.run.ID)
	require.NoError(t, err)
	assert.Equal(t, models.AgentRunStateCompleted, stored.State)
	assert.Contains(t, stored.ResponseMessagesJSON, result.Text)
	assert.Contains(t, stored.ResponseMessagesJSON, "child source")
	activities, err := fixture.base.store.ListExecutionActivities(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	assert.NotEmpty(t, activities.Items)
	for _, activity := range activities.Items {
		assert.Equal(t, models.ActivityStateCompleted, activity.State)
	}
	fixture.stop()
}

func TestBoundChildCallbacksRetainParallelReadProofs(t *testing.T) {
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
	fixture, input, _ := childModelFixture(t,
		elelemtest.Turn{Deltas: deltas}, elelemtest.Text("parallel child finished"))
	for _, path := range paths {
		writeRuntimeFile(t, filepath.Join(fixture.base.workspace, path), path)
	}
	callbacks := newChildWorkflowCallbacks(t.Context(), fixture.base.runtime, nil)
	callbacks.bind(&fixture.engine.options)
	fixture.input = input.Execution
	fixture.input.MaxConcurrentTools = len(paths)
	instance := startCompactingWorkflow(t, fixture)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(result.Context, &conversation))
	require.NotNil(t, conversation.Runtime)
	assert.Len(t, conversation.Runtime.Observations, len(paths))
	assert.Equal(t, "parallel child finished", result.Text)
	fixture.stop()
}

func TestBoundChildCallbacksKeepDirectoryHopInstructions(t *testing.T) {
	t.Parallel()
	target := t.TempDir()
	writeRuntimeFile(t, filepath.Join(target, "AGENTS.md"),
		"Instructions for the child target directory.")
	arguments, err := json.Marshal(changeDirectoryInput{Directory: target})
	require.NoError(t, err)
	fixture, input, _ := childModelFixture(t,
		elelemtest.ToolCall("childHop", toolNameChangeDirectory, string(arguments)),
		elelemtest.Text("child after hop"))
	callbacks := newChildWorkflowCallbacks(t.Context(), fixture.base.runtime, nil)
	callbacks.bind(&fixture.engine.options)
	fixture.input = input.Execution
	fixture.input.MaxConcurrentTools = 1
	instance := startCompactingWorkflow(t, fixture)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(result.Context, &conversation))
	require.NotNil(t, conversation.Runtime)
	assert.Equal(t, target, conversation.Runtime.Directory.Directory)
	assert.Contains(t, conversation.SystemPrompt,
		"Instructions for the child target directory.")
	fixture.stop()
}

func TestFailedChildBudgetRecordsAndReportsTerminalOutcome(t *testing.T) {
	t.Parallel()
	fixture, input, original := childModelFixture(t,
		elelemtest.ToolCall("overBudget", toolNameWriteFile, `{"path":"must-not-write.txt","content":"example"}`))
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(input.Execution.Context, &conversation))
	conversation.Child.MaxRounds = 1
	conversation.Child.CompletedRounds = 0
	conversation.Limits.MaxRounds = 1
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	fixture.input = input.Execution
	fixture.input.MaxRounds, fixture.input.MaxConcurrentTools = 1, 1
	fixture.input.Context = encoded
	dispatch := newWorkflowDispatch(fixture.engine.options.Lifecycle, fixture.base.runtime, fixture.engine.options.GenerationID, nil)
	dispatch.bind(&fixture.engine.options)
	instance := startNamedWorkflow(t, fixture, agentWorkflowName)
	_, err = client.GetWorkflowResult[workflowRoundResult](t.Context(), fixture.api, instance, workflowTestResultTimeout)
	require.Error(t, err)
	saved, err := fixture.base.store.GetExecution(t.Context(), fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateFailed, saved.State)
	child, err := fixture.base.store.GetAgentRun(t.Context(), fixture.input.SessionID, original.run.ID)
	require.NoError(t, err)
	assert.Equal(t, models.AgentRunStateFailed, child.State)
	assert.Equal(t, failureClassAgentRun, child.FailureClassification)
	assert.NotEmpty(t, child.FailureDetail)
	_, err = os.Stat(filepath.Join(fixture.base.workspace, "must-not-write.txt"))
	require.ErrorIs(t, err, os.ErrNotExist)
	calls, err := fixture.base.store.ListModelRuns(t.Context(), fixture.input.SessionID, session.ListModelRunsOptions{})
	require.NoError(t, err)
	assert.Len(t, calls.Items, 1)
	before, err := fixture.base.store.ListAgentRunEvents(t.Context(), fixture.input.SessionID, original.run.ID, session.ListAgentRunEventsOptions{})
	require.NoError(t, err)
	activities, err := fixture.base.store.ListExecutionActivities(t.Context(), fixture.input.SessionID, fixture.input.ExecutionID, session.ListEventsOptions{})
	require.NoError(t, err)
	var final workflowFinishInput
	found := false
	for _, activity := range activities.Items {
		if activity.Name == session.ExecutionFinishActivityName {
			require.NoError(t, json.Unmarshal([]byte(activity.InputJSON), &final))
			found = true
		}
	}
	require.True(t, found)
	retry, err := fixture.engine.finishExecution(t.Context(), final)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, retry.Decision)
	after, err := fixture.base.store.ListAgentRunEvents(t.Context(), fixture.input.SessionID, original.run.ID, session.ListAgentRunEventsOptions{})
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestChildFinishAdmissionKeepsOrdinaryRoundBounds(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		state   models.ExecutionState
		offset  int
		wantErr error
	}{
		{name: "failure terminal marker", state: models.ExecutionStateFailed},
		{name: "completed terminal marker refused", state: models.ExecutionStateCompleted, wantErr: commerr.ErrValidationFailed},
		{name: "failure past terminal marker refused", state: models.ExecutionStateFailed, offset: 1, wantErr: commerr.ErrValidationFailed},
		{name: "completed last model round", state: models.ExecutionStateCompleted, offset: -1},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, input, _ := childModelFixture(t)
			callbacks := newChildWorkflowCallbacks(t.Context(), fixture.base.runtime, nil)
			callbacks.bind(&fixture.engine.options)
			result, err := callbacks.beforeFinish(t.Context(), workflowFinishInput{
				Execution: input.Execution, Round: input.Execution.MaxRounds + tc.offset,
				Result: workflowFinishResult{Result: workflowRoundResult{Context: input.Execution.Context}, Finish: session.FinishExecutionInput{State: tc.state}},
			})
			require.ErrorIs(t, err, tc.wantErr)
			if tc.wantErr != nil {
				return
			}
			require.NotNil(t, result.Finish.Child)
			if tc.state == models.ExecutionStateFailed {
				assert.Equal(t, models.AgentRunStateFailed, result.Finish.Child.State)
			} else {
				assert.Equal(t, models.AgentRunStateCompleted, result.Finish.Child.State)
			}
		})
	}
}
