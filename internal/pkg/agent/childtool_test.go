package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChildWorkflowToolPersistsReadAndReusesSavedResult(t *testing.T) {
	t.Parallel()
	fixture, round, sink := childModelFixture(t,
		elelemtest.ToolCall(runtimeToolCallID, toolNameReadFile,
			runtimeToolArguments(t, runtimeToolFileName)))
	path := filepath.Join(fixture.base.workspace, runtimeToolFileName)
	writeRuntimeFile(t, path, runtimeToolFileBody)
	model, err := fixture.engine.modelRound(t.Context(), round)
	require.NoError(t, err)
	round.Execution.Context = model.Output.Context
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(round.Execution.Context, &conversation))
	scope := childPreparationScopeForTest(t, fixture, conversation)
	var toolErr error

	fixture.engine.options.ToolCall = func(
		ctx context.Context, input workflowToolInput,
	) (workflowToolResult, error) {
		result, err := fixture.base.runtime.workflowChildToolCall(ctx, sink,
			scope, input)
		toolErr = err

		return result, err
	}
	input := workflowToolInput{Round: round, Call: model.Output.Calls[0]}
	first, err := fixture.engine.toolCall(t.Context(), input)
	require.NoError(t, err)
	require.NoError(t, toolErr)
	require.Equal(t, session.ActivityDecisionExecute, first.Decision)
	assert.False(t, first.Output.Result.IsError)
	writeRuntimeFile(t, path, "changed after completed read")
	fixture.stop()
	require.NoError(t, fixture.base.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{
		Directory: fixture.base.stateDirectory,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)
	fixture.engine.options.Store = store
	fixture.engine.options.GenerationID = uuid.New()
	fixture.engine.options.Lifecycle = t.Context()
	reused, err := fixture.engine.toolCall(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	assert.Equal(t, first.Output.Result, reused.Output.Result)
	var read tools.ReadFileOutput
	require.NoError(t, json.Unmarshal([]byte(reused.Output.Result.Content), &read))
	assert.Equal(t, runtimeToolFileBody, read.Content)
	require.NoError(t, json.Unmarshal(reused.Output.Context, &conversation))
	require.Len(t, conversation.Runtime.Observations, 1)
	assert.Equal(t, path, conversation.Runtime.Observations[0].Path)
	history, err := store.AgentRunHistory(t.Context(), sink.sessionID,
		sink.run.ID)
	require.NoError(t, err)
	require.Len(t, history.Messages, 3)
	assert.Equal(t, models.MessageRoleTool, history.Messages[2].Role)
	assert.Equal(t, runtimeToolCallID, history.Messages[2].ToolCallID)
}

func TestChildWorkflowToolDirectoryHopRetainsNewHarness(t *testing.T) {
	t.Parallel()
	target := t.TempDir()
	writeRuntimeFile(t, filepath.Join(target, "AGENTS.md"),
		"Instructions for the child target directory.")
	arguments, err := json.Marshal(changeDirectoryInput{Directory: target})
	require.NoError(t, err)
	fixture, round, sink := childModelFixture(t,
		elelemtest.ToolCall("childHop", toolNameChangeDirectory, string(arguments)))
	model, err := fixture.engine.modelRound(t.Context(), round)
	require.NoError(t, err)
	round.Execution.Context = model.Output.Context
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(round.Execution.Context, &conversation))
	scope := childPreparationScopeForTest(t, fixture, conversation)
	fixture.engine.options.ToolCall = func(
		ctx context.Context, input workflowToolInput,
	) (workflowToolResult, error) {
		return fixture.base.runtime.workflowChildToolCall(ctx, sink, scope, input)
	}
	result, err := fixture.engine.toolCall(t.Context(), workflowToolInput{
		Round: round, Call: model.Output.Calls[0],
	})
	require.NoError(t, err)
	require.Equal(t, session.ActivityDecisionExecute, result.Decision)
	require.False(t, result.Output.Result.IsError, result.Output.Result.Content)
	require.NoError(t, json.Unmarshal(result.Output.Context, &conversation))
	assert.Equal(t, target, conversation.Runtime.Directory.Directory)
	assert.Contains(t, conversation.SystemPrompt,
		"Instructions for the child target directory.")
	assert.Equal(t, fixture.base.workspace, conversation.Child.Workspace)
	history, err := fixture.base.store.AgentRunHistory(t.Context(), sink.sessionID,
		sink.run.ID)
	require.NoError(t, err)
	require.Len(t, history.Messages, 3)
	stored, err := fixture.base.store.GetAgentRun(t.Context(), sink.sessionID,
		sink.run.ID)
	require.NoError(t, err)
	assert.Equal(t, target, stored.Workspace)
}

func TestChildWorkflowToolRefusesMissingLeaseAndForeignScope(t *testing.T) {
	t.Parallel()
	fixture, round, sink := childModelFixture(t,
		elelemtest.ToolCall("childRead", toolNameReadFile, `{"path":"missing"}`))
	model, err := fixture.engine.modelRound(t.Context(), round)
	require.NoError(t, err)
	round.Execution.Context = model.Output.Context
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(round.Execution.Context, &conversation))
	scope := childPreparationScopeForTest(t, fixture, conversation)
	input := workflowToolInput{Round: round, Call: model.Output.Calls[0]}
	_, err = fixture.base.runtime.workflowChildToolCall(t.Context(), sink,
		scope, input)
	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
	foreign := uuid.New()
	scope.options.agentRunID = &foreign
	_, err = fixture.base.runtime.workflowChildToolCall(t.Context(), sink,
		scope, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	history, err := fixture.base.store.AgentRunHistory(t.Context(), sink.sessionID,
		sink.run.ID)
	require.NoError(t, err)
	assert.Len(t, history.Messages, 2)
}

func TestChildWorkflowToolRecordsHandlerFailures(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		tool      string
		arguments string
	}{
		{
			name: "missing file", tool: toolNameReadFile,
			arguments: `{"path":"missing-file.txt"}`,
		},
		{name: "unknown tool", tool: "unknownChildTool", arguments: `{}`},
		{name: "malformed arguments", tool: toolNameReadFile, arguments: `[]`},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, round, sink := childModelFixture(t,
				elelemtest.ToolCall("childFailure", tc.tool, tc.arguments))
			model, err := fixture.engine.modelRound(t.Context(), round)
			require.NoError(t, err)
			round.Execution.Context = model.Output.Context
			var conversation workflowConversation
			require.NoError(t, json.Unmarshal(round.Execution.Context, &conversation))
			scope := childPreparationScopeForTest(t, fixture, conversation)
			fixture.engine.options.ToolCall = func(
				ctx context.Context, input workflowToolInput,
			) (workflowToolResult, error) {
				return fixture.base.runtime.workflowChildToolCall(ctx, sink, scope,
					input)
			}
			result, err := fixture.engine.toolCall(t.Context(), workflowToolInput{
				Round: round, Call: model.Output.Calls[0],
			})
			require.NoError(t, err)
			require.Equal(t, session.ActivityDecisionExecute, result.Decision)
			assert.True(t, result.Output.Result.IsError)
			history, err := fixture.base.store.AgentRunHistory(t.Context(),
				sink.sessionID, sink.run.ID)
			require.NoError(t, err)
			require.Len(t, history.Messages, 3)
			assert.True(t, history.Messages[2].IsError)
		})
	}
}
