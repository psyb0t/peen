package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cschleiden/go-workflows/client"
	"github.com/google/uuid"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/essessey"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootWorkflowParallelReadSnapshotsSurviveReverseCompletion(t *testing.T) {
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
	driver := elelemtest.NewScriptedDriver(elelemtest.Turn{Deltas: deltas},
		elelemtest.Text(runtimeToolFinalText))
	fixture, _ := newRootModelWorkflowFixture(t, driver)
	fixture.input.MaxConcurrentTools = 2
	for _, path := range paths {
		writeRuntimeFile(t, filepath.Join(fixture.base.workspace, path), path)
	}
	orderRootWorkflowToolSnapshots(t, &fixture)
	preparedContext := make(chan json.RawMessage, 1)
	original := fixture.engine.options.PrepareRound
	fixture.engine.options.PrepareRound = func(
		ctx context.Context, input workflowRoundInput,
	) (json.RawMessage, error) {
		if input.Round == 1 {
			preparedContext <- input.Execution.Context
		}

		return original(ctx, input)
	}
	instance := fixture.start(t)
	_, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, 10*time.Second)
	require.NoError(t, err)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(<-preparedContext, &conversation))
	require.NotNil(t, conversation.Runtime)
	require.Len(t, conversation.Runtime.Observations, 2)
	for index, path := range paths {
		entry := conversation.Runtime.Observations[index]
		assert.Equal(t, filepath.Join(fixture.base.workspace, path), entry.Path)
		assert.True(t, entry.HasHash)
		assert.Positive(t, entry.Revision)
	}
	assert.Equal(t, 2, driver.Calls())
	fixture.stop()
}

func TestRootWorkflowToolSavesDeliveredSkills(t *testing.T) {
	t.Parallel()
	const skill = "core:planning"
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("loadProcedure", toolNameUseSkill,
			`{"name":"core:planning"}`),
		elelemtest.Text(runtimeToolFinalText),
	)
	fixture, _ := newRootModelWorkflowFixture(t, driver)
	instance := fixture.start(t)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, 10*time.Second)
	require.NoError(t, err)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(result.Context, &conversation))
	require.NotNil(t, conversation.Runtime)
	require.NotNil(t, conversation.Runtime.Skills)
	assert.Contains(t, conversation.Runtime.Skills.Delivered, skill)
	restored, err := restoreWorkflowSkills(conversation.Runtime.Skills)
	require.NoError(t, err)
	assert.Contains(t, restored.held(), skill)
	restored.forgetThrough(100)
	assert.Contains(t, restored.held(), skill)
	assert.Equal(t, 2, driver.Calls())
	fixture.stop()
}

func orderRootWorkflowToolSnapshots(t *testing.T, fixture *executionLoopFixture) {
	t.Helper()
	firstSnapshot, secondSnapshot := make(chan struct{}), make(chan struct{})
	original := fixture.engine.options.ToolCall
	fixture.engine.options.ToolCall = func(
		ctx context.Context, input workflowToolInput,
	) (workflowToolResult, error) {
		if input.Index == 0 {
			select {
			case <-firstSnapshot:
			case <-ctx.Done():
				return workflowToolResult{}, ctx.Err()
			}
		}
		result, err := original(ctx, input)
		if input.Index == 0 {
			close(secondSnapshot)

			return result, err
		}
		close(firstSnapshot)
		select {
		case <-secondSnapshot:
		case <-ctx.Done():
			return workflowToolResult{}, ctx.Err()
		}

		return result, err
	}
}

func TestRootWorkflowToolPersistsTranscriptStreamAndReadState(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(runtimeToolCallID, toolNameReadFile,
			runtimeToolArguments(t, runtimeToolFileName)),
		elelemtest.Text(runtimeToolFinalText),
	)
	fixture, _ := newRootModelWorkflowFixture(t, driver)
	path := filepath.Join(fixture.base.workspace, runtimeToolFileName)
	writeRuntimeFile(t, path, runtimeToolFileBody)
	instance := fixture.start(t)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, runtimeToolFinalText, result.Text)
	messages, err := fixture.base.store.ListMessages(t.Context(),
		fixture.input.SessionID, session.ListMessagesOptions{})
	require.NoError(t, err)
	require.Len(t, messages.Items, 4)
	assert.Equal(t, models.MessageRoleTool, messages.Items[2].Role)
	assert.Equal(t, runtimeToolCallID, messages.Items[2].ToolCallID)
	var read tools.ReadFileOutput
	require.NoError(t, json.Unmarshal([]byte(messages.Items[2].Content), &read))
	assert.Equal(t, runtimeToolFileBody, read.Content)
	page, err := fixture.base.store.ListExecutionActivities(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	handler := rootReadHandlerActivity(t, page)
	var contextAfter workflowConversation
	require.NoError(t, json.Unmarshal(
		[]byte(handler.ContextJSON), &contextAfter))
	require.NotNil(t, contextAfter.Runtime)
	assert.Equal(t, fixture.base.workspace,
		contextAfter.Runtime.Directory.Directory)
	require.Len(t, contextAfter.Runtime.Observations, 1)
	assert.Equal(t, path, contextAfter.Runtime.Observations[0].Path)
	assert.True(t, contextAfter.Runtime.Observations[0].HasHash)
	events, err := fixture.base.store.ListEvents(t.Context(),
		fixture.input.SessionID, session.ListEventsOptions{})
	require.NoError(t, err)
	toolUse, toolResult := false, false
	for _, event := range events.Items {
		if event.EventType != essessey.EventTypeContentBlockStart {
			continue
		}

		var block essessey.ContentBlockStartData
		require.NoError(t, json.Unmarshal([]byte(event.PayloadJSON), &block))
		toolUse = toolUse || block.ContentBlock.Type ==
			essessey.ContentBlockTypeToolUse
		toolResult = toolResult || block.ContentBlock.Type ==
			essessey.ContentBlockTypeToolResult
	}
	assert.True(t, toolUse)
	assert.True(t, toolResult)
	requests := driver.Requests()
	require.Len(t, requests, 2)
	assert.Equal(t, elelem.RoleTool,
		requests[1].Messages[len(requests[1].Messages)-1].Role)
	fixture.stop()
}

func TestRootWorkflowToolReusesSavedReadAfterDatabaseReopen(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(runtimeToolCallID, toolNameReadFile,
			runtimeToolArguments(t, runtimeToolFileName)),
		elelemtest.Text(runtimeToolFinalText),
	)
	fixture, prepared := newRootModelWorkflowFixture(t, driver)
	path := filepath.Join(fixture.base.workspace, runtimeToolFileName)
	writeRuntimeFile(t, path, runtimeToolFileBody)
	instance := fixture.start(t)
	_, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, 10*time.Second)
	require.NoError(t, err)
	page, err := fixture.base.store.ListExecutionActivities(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	handler := rootReadHandlerActivity(t, page)
	var input workflowToolPhaseInput
	require.NoError(t, json.Unmarshal([]byte(handler.InputJSON), &input))
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
	require.NoError(t, os.WriteFile(path, []byte("changed after completion"),
		runtimeTestFileMode))
	writeRuntimeFile(t, filepath.Join(fixture.base.workspace, "AGENTS.md"),
		"Changed after checkpoint.")
	reused, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	var read tools.ReadFileOutput
	require.NoError(t, json.Unmarshal([]byte(reused.Output.Outcome.Result.Content), &read))
	assert.Equal(t, runtimeToolFileBody, read.Content)
	assert.Equal(t, 2, driver.Calls())
	var contextAfter workflowConversation
	require.NoError(t, json.Unmarshal(reused.Output.Outcome.Context, &contextAfter))
	require.NotNil(t, contextAfter.Runtime)
	savedHarness, err := harness.RestoreCheckpoint(contextAfter.Runtime.Harness)
	require.NoError(t, err)
	assert.Equal(t, prepared.snapshot.Hash(), savedHarness.Hash())
	freshHarness, err := fixture.base.runtime.resolveDirectory(fixture.base.workspace)
	require.NoError(t, err)
	assert.NotEqual(t, savedHarness.Hash(), freshHarness.Hash())
	executor, err := tools.NewExecutor(tools.Options{
		Workspace: fixture.base.workspace,
	})
	require.NoError(t, err)
	require.NoError(t, executor.RestoreObservations(
		contextAfter.Runtime.Observations))
	_, err = executor.EditFile(t.Context(), tools.EditFileInput{
		Path: path, Edits: []tools.TextEdit{{Old: "changed", New: "edited"}},
	})
	require.ErrorIs(t, err, tools.ErrStaleHash)
	messages, err := store.ListMessages(t.Context(), fixture.input.SessionID,
		session.ListMessagesOptions{})
	require.NoError(t, err)
	assert.Len(t, messages.Items, 4)
}

func rootReadHandlerActivity(t *testing.T,
	page *session.ExecutionActivityPage,
) *models.ExecutionActivity {
	t.Helper()
	var handler *models.ExecutionActivity
	for _, activity := range page.Items {
		require.Equal(t, models.ActivityStateCompleted, activity.State)
		if activity.StepID == "tool:0:0:handler" {
			require.Nil(t, handler)
			handler = activity
		}
	}
	require.NotNil(t, handler)
	assert.Equal(t, toolPhaseActivityName, handler.Name)

	return handler
}
