package agent

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/cschleiden/go-workflows/client"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/essessey"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const rootWorkflowProbeTool = "workflowProbe"

func newRootModelWorkflowFixture(
	t *testing.T,
	driver elelem.Driver,
	extraTools ...elelem.Tool,
) (executionLoopFixture, *preparedTurn) {
	t.Helper()

	return newRootModelWorkflowFixtureWithOptions(t, driver, nil,
		extraTools...)
}

func newRootModelWorkflowFixtureWithOptions(
	t *testing.T,
	driver elelem.Driver,
	customize func(*RuntimeOptions),
	extraTools ...elelem.Tool,
) (executionLoopFixture, *preparedTurn) {
	t.Helper()
	base := newRuntimeFixtureWithOptions(t, driver, customize)
	base.runtime.extraTools = extraTools
	prepared, err := base.runtime.prepareTurn(t.Context(), TurnRequest{
		RequestID: uuid.New(), Workspace: base.workspace,
		Message: "inspect the example workspace",
	})
	require.NoError(t, err)
	prepared.attachPublisher(t.Context())
	require.NoError(t, base.runtime.registerActiveUserMessageQueue(prepared))
	t.Cleanup(func() { base.runtime.closeActiveUserMessageQueue(prepared) })
	require.NoError(t, base.runtime.startLease(t.Context(), prepared))
	scope, err := base.runtime.rootDirectoryScope(t.Context(), prepared)
	require.NoError(t, err)
	conversation, err := base.runtime.rootWorkflowConversation(prepared, scope)
	require.NoError(t, err)
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	execution, err := base.store.CreateExecution(t.Context(),
		prepared.opened.Session.ID, session.CreateExecutionInput{
			ID: uuid.New(), RootTurnID: prepared.lease.TurnID,
			WorkflowVersion: agentWorkflowName, Task: prepared.input.Message,
			WorkingDirectory: prepared.workspace, Context: encoded,
		})
	require.NoError(t, err)
	fixture := newExecutionLoopFixtureWithInput(t, base,
		workflowExecutionInput{
			SessionID: prepared.opened.Session.ID, ExecutionID: execution.ID,
			MaxRounds:          base.runtime.maxToolRounds,
			MaxConcurrentTools: base.runtime.maxConcurrentTools, Context: encoded,
		}, true)
	require.NoError(t, base.runtime.bindRootWorkflowInbox(t.Context(),
		prepared, execution.ID))
	fixture.engine.options.ModelRound = func(
		ctx context.Context, input workflowRoundInput,
	) (workflowRoundResult, error) {
		return base.runtime.workflowRootModelRound(ctx, prepared, input)
	}
	fixture.engine.options.ToolCall = func(
		ctx context.Context, input workflowToolInput,
	) (workflowToolResult, error) {
		return base.runtime.workflowRootToolCall(ctx, prepared, scope, input)
	}
	fixture.engine.options.PrepareRound = func(
		ctx context.Context, input workflowRoundInput,
	) (json.RawMessage, error) {
		return base.runtime.prepareRootWorkflowRound(ctx, prepared, scope, input)
	}

	return fixture, prepared
}

func TestRootWorkflowModelPersistsAuditAndContentBlocks(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Thinking(
		"inspect saved state", "finished example task"))
	fixture, prepared := newRootModelWorkflowFixture(t, driver)
	instance := fixture.start(t)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, "finished example task", result.Text)
	runs, err := fixture.base.store.ListModelRuns(t.Context(),
		fixture.input.SessionID, session.ListModelRunsOptions{})
	require.NoError(t, err)
	require.Len(t, runs.Items, 1)
	assert.Equal(t, prepared.lease.TurnID, runs.Items[0].TurnID)
	assert.Equal(t, models.ModelRunStateCompleted, runs.Items[0].State)
	var settings rootWorkflowModelAuditSettings
	require.NoError(t, json.Unmarshal(
		[]byte(runs.Items[0].RequestSettingsJSON), &settings))
	assert.False(t, settings.AutoToolCalls)
	assert.Equal(t, workflowManualRequestRoundLimit, settings.MaxRounds)
	assert.Equal(t, fixture.input.ExecutionID, settings.ExecutionID)
	assert.Equal(t, fixture.engine.options.GenerationID,
		settings.WorkerGenerationID)
	assert.NotEqual(t, uuid.Nil, settings.ActivityID)
	assert.EqualValues(t, 1, settings.Attempt)
	calls, err := fixture.base.store.ListModelCalls(t.Context(),
		fixture.input.SessionID, runs.Items[0].ID, session.ListModelCallsOptions{})
	require.NoError(t, err)
	require.Len(t, calls.Items, 1)
	assert.Equal(t, models.ModelCallStateCompleted, calls.Items[0].State)
	assert.Contains(t, calls.Items[0].ResponseMessageJSON, result.Text)
	messages, err := fixture.base.store.ListMessages(t.Context(),
		fixture.input.SessionID, session.ListMessagesOptions{})
	require.NoError(t, err)
	require.Len(t, messages.Items, 2)
	assert.Equal(t, models.MessageRoleAssistant, messages.Items[1].Role)
	assert.Equal(t, result.Text, messages.Items[1].Content)
	events, err := fixture.base.store.ListEvents(t.Context(),
		fixture.input.SessionID, session.ListEventsOptions{})
	require.NoError(t, err)
	deltas := 0
	for _, event := range events.Items {
		if event.EventType == essessey.EventTypeContentBlockDelta {
			deltas++
		}
	}
	assert.Positive(t, deltas)
	assert.Equal(t, 1, prepared.adapter.Rounds())
	fixture.stop()
}

func TestRootWorkflowModelDoesNotExecuteReturnedTools(t *testing.T) {
	t.Parallel()
	var effects atomic.Int32
	driver := elelemtest.NewScriptedDriver(elelemtest.ToolCall(
		"exampleCall", rootWorkflowProbeTool, `{}`))
	fixture, _ := newRootModelWorkflowFixture(t, driver, elelem.Tool{
		Name:            rootWorkflowProbeTool,
		ArgumentsSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(context.Context, elelem.ToolInput) (elelem.ToolResult, error) {
			effects.Add(1)

			return elelem.ToolResult{Content: "effect ran"}, nil
		},
	})
	result, err := fixture.engine.modelRound(t.Context(), workflowRoundInput{
		Execution: fixture.input,
	})
	require.NoError(t, err)
	require.Len(t, result.Output.Calls, 1)
	assert.Equal(t, rootWorkflowProbeTool, result.Output.Calls[0].Name)
	assert.Zero(t, effects.Load())
	assert.Equal(t, 1, driver.Calls())
	reused, err := fixture.engine.modelRound(t.Context(), workflowRoundInput{
		Execution: fixture.input,
	})
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	assert.Equal(t, 1, driver.Calls())
}

func TestRootWorkflowModelRejectsMismatchedInput(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		change  func(*workflowRoundInput, *preparedTurn)
		wantErr error
	}{
		{
			name: "session", wantErr: commerr.ErrPermissionDenied,
			change: func(input *workflowRoundInput, _ *preparedTurn) {
				input.Execution.SessionID = uuid.New()
			},
		},
		{
			name: "execution", wantErr: commerr.ErrNotFound,
			change: func(input *workflowRoundInput, _ *preparedTurn) {
				input.Execution.ExecutionID = uuid.New()
			},
		},
		{
			name: "turn", wantErr: commerr.ErrConflict,
			change: func(_ *workflowRoundInput, prepared *preparedTurn) {
				prepared.lease.TurnID = uuid.New()
			},
		},
		{
			name: "negative round", wantErr: commerr.ErrValidationFailed,
			change: func(input *workflowRoundInput, _ *preparedTurn) {
				input.Round = -1
			},
		},
		{
			name: "round limit", wantErr: commerr.ErrValidationFailed,
			change: func(input *workflowRoundInput, _ *preparedTurn) {
				input.Round = input.Execution.MaxRounds
			},
		},
		{
			name: "context", wantErr: commerr.ErrValidationFailed,
			change: func(input *workflowRoundInput, _ *preparedTurn) {
				input.Execution.Context = json.RawMessage(`{`)
			},
		},
		{
			name: "compaction version", wantErr: commerr.ErrValidationFailed,
			change: func(input *workflowRoundInput, _ *preparedTurn) {
				input.Execution.Context = json.RawMessage(
					`{"compaction":{"version":0}}`,
				)
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			driver := elelemtest.NewScriptedDriver()
			fixture, prepared := newRootModelWorkflowFixture(t, driver)
			input := workflowRoundInput{Execution: fixture.input}
			tc.change(&input, prepared)
			_, err := fixture.base.runtime.workflowRootModelRound(t.Context(),
				prepared, input)
			require.ErrorIs(t, err, tc.wantErr)
			assert.Zero(t, driver.Calls())
			runs, err := fixture.base.store.ListModelRuns(t.Context(),
				fixture.input.SessionID, session.ListModelRunsOptions{})
			require.NoError(t, err)
			assert.Empty(t, runs.Items)
		})
	}
}

func TestRootWorkflowModelUsesSavedCompletionMode(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("completed response"))
	fixture, _ := newRootModelWorkflowFixture(t, driver)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(fixture.input.Context, &conversation))
	streaming := false
	conversation.Streaming = &streaming
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	fixture.input.Context = encoded
	instance := fixture.start(t)
	_, err = client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, []bool{false}, driver.Streamed())
	runs, err := fixture.base.store.ListModelRuns(t.Context(),
		fixture.input.SessionID, session.ListModelRunsOptions{})
	require.NoError(t, err)
	require.Len(t, runs.Items, 1)
	var settings rootWorkflowModelAuditSettings
	require.NoError(t, json.Unmarshal(
		[]byte(runs.Items[0].RequestSettingsJSON), &settings))
	assert.False(t, settings.Streaming)
	fixture.stop()
}
