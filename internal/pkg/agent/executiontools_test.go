package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutionToolsRejectsHistoryBeyondTheResultBudget(t *testing.T) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), false)
	fixture.base.runtime.maxToolResultTokens = 1
	read := retainedExecutionTool(t, fixture, "readExecution")
	result := callRetainedExecutionTool(t, read,
		map[string]any{"executionId": fixture.input.ExecutionID})
	require.True(t, result.IsError)
	assert.Contains(t, result.Content, "history page too large")
}

func retainedExecutionTool(t *testing.T, fixture executionLoopFixture, name string) elelem.Tool {
	t.Helper()
	deps := &launchAgentDeps{runtime: fixture.base.runtime, sessionID: fixture.input.SessionID}
	for _, tool := range messageTools(deps, nil) {
		if tool.Name == name {
			require.True(t, json.Valid(tool.ArgumentsSchema))

			return tool
		}
	}
	t.Fatalf("retained execution tool %q is missing", name)

	return elelem.Tool{}
}

func callRetainedExecutionTool(t *testing.T, tool elelem.Tool, arguments any) elelem.ToolResult {
	t.Helper()
	encoded, err := json.Marshal(arguments)
	require.NoError(t, err)
	result, err := tool.Handler(t.Context(), elelem.ToolInput{CallID: "historyRead", Arguments: encoded})
	require.NoError(t, err)

	return result
}

func TestExecutionToolsSearchPausedHistoryAcrossPages(t *testing.T) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), false)
	root, err := fixture.base.store.GetExecution(t.Context(), fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	search := retainedExecutionTool(t, fixture, "searchExecutions")
	ids := make(map[uuid.UUID]struct{}, 13)
	for range 13 {
		run, err := fixture.base.store.CreateAgentRun(t.Context(), fixture.input.SessionID, session.StartAgentRunInput{
			ID: uuid.New(), ParentTurnID: root.RootTurnID, RequestID: uuid.New(),
			Name: "historyChild", Definition: models.AgentRunDefinitionAdHoc, Depth: 1,
			Workspace: fixture.base.workspace, ModelReference: runtimeTestModelReference,
			ModelID: runtimeTestModelID, Task: "retained child", Instructions: "inspect retained work",
			AllowedToolsJSON: `[]`, SystemPrompt: "retained child instructions",
		})
		require.NoError(t, err)
		child, err := fixture.base.store.CreateExecution(t.Context(), fixture.input.SessionID, session.CreateExecutionInput{
			ID: uuid.New(), RootTurnID: root.RootTurnID, ParentExecutionID: &fixture.input.ExecutionID,
			AgentRunID: &run.ID, WorkflowVersion: agentWorkflowName, Task: "retained child",
			WorkingDirectory: fixture.base.workspace, Context: json.RawMessage(`{"progress":"retained checkpoint"}`),
		})
		require.NoError(t, err)
		ids[child.ID] = struct{}{}
	}
	_, err = fixture.base.store.PauseExecution(t.Context(), fixture.input.SessionID, fixture.input.ExecutionID, "user stopped retained work")
	require.NoError(t, err)
	before, err := fixture.base.store.GetExecution(t.Context(), fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	for _, offset := range []int{0, 5, 10, 13, 99} {
		result := callRetainedExecutionTool(t, search, map[string]any{
			"parentId": fixture.input.ExecutionID, "state": models.ExecutionStatePaused,
			"text": "retained checkpoint", "limit": 5, "offset": offset,
		})
		require.False(t, result.IsError, "%s", result.Content)
		var page session.ExecutionPage
		require.NoError(t, json.Unmarshal([]byte(result.Content), &page))
		count := min(5, max(0, 13-offset))
		require.Len(t, page.Items, count)
		assert.Equal(t, offset+count < 13, page.HasMore)
		for _, item := range page.Items {
			_, found := ids[item.ID]
			require.True(t, found, "page repeats or invents a record")
			delete(ids, item.ID)
			assert.Empty(t, item.ContextJSON, "search pages do not dump saved conversations")
			assert.Equal(t, models.ExecutionStatePaused, item.State)
		}
	}
	assert.Empty(t, ids)
	after, err := fixture.base.store.GetExecution(t.Context(), fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestExecutionToolsSearchEveryRetainedState(t *testing.T) {
	t.Parallel()
	testCases := []models.ExecutionState{
		models.ExecutionStateQueued, models.ExecutionStateRunning,
		models.ExecutionStatePaused, models.ExecutionStateReconciling,
		models.ExecutionStateCompleted, models.ExecutionStateFailed,
		models.ExecutionStateCancelled,
	}
	for _, tc := range testCases {
		t.Run(string(tc), func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), false)
			repository := repositories.Use(fixture.base.handle.GormDB).Execution
			_, err := repository.WithContext(t.Context()).Where(repository.ID.Eq(fixture.input.ExecutionID)).
				UpdateSimple(repository.State.Value(string(tc)))
			require.NoError(t, err)
			search := retainedExecutionTool(t, fixture, "searchExecutions")
			result := callRetainedExecutionTool(t, search, map[string]any{"id": fixture.input.ExecutionID, "state": tc})
			require.False(t, result.IsError, "%s", result.Content)
			var page session.ExecutionPage
			require.NoError(t, json.Unmarshal([]byte(result.Content), &page))
			require.Len(t, page.Items, 1)
			assert.Equal(t, tc, page.Items[0].State)
			assert.False(t, page.HasMore)
			result = callRetainedExecutionTool(t, search, map[string]any{"text": "' OR 1=1 --"})
			require.False(t, result.IsError)
			require.NoError(t, json.Unmarshal([]byte(result.Content), &page))
			assert.Empty(t, page.Items)
			assert.False(t, page.HasMore)
		})
	}
}

func TestExecutionToolsReadSavedStateAndEveryHistoryPage(t *testing.T) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), false)
	read := retainedExecutionTool(t, fixture, "readExecution")
	activityIDs := make([]uuid.UUID, 0, 13)
	for index := range 13 {
		claim, err := fixture.base.store.BeginExecutionActivity(t.Context(), fixture.input.SessionID,
			fixture.input.ExecutionID, fixture.engine.options.GenerationID, session.ScheduleActivityInput{
				StepID: fmt.Sprintf("historyModel%d", index), Name: "retainedModel", Kind: models.ActivityKindModel,
				ReplayPolicy: models.ActivityReplayPolicyRetry, Input: json.RawMessage(`{"prompt":"retained input"}`),
			})
		require.NoError(t, err)
		activityIDs = append(activityIDs, claim.Activity.ID)
		if index == 0 {
			_, err = fixture.base.store.CompleteExecutionActivity(t.Context(), fixture.input.SessionID, claim.Lease,
				session.CompleteActivityInput{Output: json.RawMessage(`{"answer":"retained output"}`), Context: fixture.input.Context})
			require.NoError(t, err)
		}
	}
	_, err := fixture.base.store.PauseExecution(t.Context(), fixture.input.SessionID, fixture.input.ExecutionID, "retain this state")
	require.NoError(t, err)
	before, err := fixture.base.store.GetExecution(t.Context(), fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	result := callRetainedExecutionTool(t, read, map[string]any{"executionId": before.ID})
	require.False(t, result.IsError, "%s", result.Content)
	var saved struct {
		Execution *models.Execution `json:"execution"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Content), &saved))
	expected := *before
	expected.ContextJSON = ""
	assert.Equal(t, &expected, saved.Execution)
	for _, part := range []string{"activities", "events"} {
		count := 13
		if part == "events" {
			count = int(before.EventCount)
		}
		for offset := 0; offset <= count+5; offset += 5 {
			result := callRetainedExecutionTool(t, read, map[string]any{"executionId": before.ID, "part": part, "limit": 5, "offset": offset})
			require.False(t, result.IsError, "%s", result.Content)
			var page struct {
				Activities *session.ExecutionActivityPage `json:"activities"`
				Events     *session.ExecutionEventPage    `json:"events"`
			}
			require.NoError(t, json.Unmarshal([]byte(result.Content), &page))
			want := min(5, max(0, count-offset))
			if part == "activities" {
				require.NotNil(t, page.Activities)
				require.Len(t, page.Activities.Items, want)
				assert.Equal(t, offset+want < count, page.Activities.HasMore)
				for index, item := range page.Activities.Items {
					assert.Equal(t, activityIDs[offset+index], item.ID)
				}
				if offset == 0 {
					require.NotNil(t, page.Activities.Items[0].OutputJSON)
					assert.Contains(t, *page.Activities.Items[0].OutputJSON, "retained output")
				}
			} else {
				require.NotNil(t, page.Events)
				require.Len(t, page.Events.Items, want)
				assert.Equal(t, offset+want < count, page.Events.HasMore)
				for index, item := range page.Events.Items {
					assert.EqualValues(t, offset+index+1, item.Sequence)
				}
			}
		}
	}
	result = callRetainedExecutionTool(t, read, map[string]any{"executionId": before.ID, "part": "inbox"})
	require.False(t, result.IsError, "%s", result.Content)
	after, err := fixture.base.store.GetExecution(t.Context(), fixture.input.SessionID, before.ID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestExecutionToolsContextPagesReconstructRetainedConversation(t *testing.T) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), false)
	read := retainedExecutionTool(t, fixture, "readExecution")
	contextJSON := json.RawMessage(`{"progress":"retained 🦊 conversation"}`)
	claim, err := fixture.base.store.BeginExecutionActivity(t.Context(), fixture.input.SessionID,
		fixture.input.ExecutionID, fixture.engine.options.GenerationID, session.ScheduleActivityInput{
			StepID: "retainedContext", Kind: models.ActivityKindModel, Name: "retainedModel",
			ReplayPolicy: models.ActivityReplayPolicyRetry, Input: json.RawMessage(`{}`),
		})
	require.NoError(t, err)
	_, err = fixture.base.store.CompleteExecutionActivity(t.Context(), fixture.input.SessionID, claim.Lease,
		session.CompleteActivityInput{Output: json.RawMessage(`{}`), Context: contextJSON})
	require.NoError(t, err)
	_, err = fixture.base.store.PauseExecution(t.Context(), fixture.input.SessionID, fixture.input.ExecutionID, "retain context pages")
	require.NoError(t, err)
	before, err := fixture.base.store.GetExecution(t.Context(), fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	var chunks []string
	for offset := 0; ; {
		result := callRetainedExecutionTool(t, read, map[string]any{"executionId": before.ID, "activityId": claim.Activity.ID, "part": "context", "limit": 7, "offset": offset})
		require.False(t, result.IsError, "%s", result.Content)
		var output struct {
			Context struct {
				Content    string `json:"content"`
				NextOffset int    `json:"nextOffset"`
				HasMore    bool   `json:"hasMore"`
			} `json:"context"`
		}
		require.NoError(t, json.Unmarshal([]byte(result.Content), &output))
		page := output.Context
		require.True(t, utf8.ValidString(page.Content))
		require.LessOrEqual(t, len(page.Content), 7)
		chunks = append(chunks, page.Content)
		if !page.HasMore {
			assert.Equal(t, len(contextJSON), page.NextOffset)

			break
		}
		require.Greater(t, page.NextOffset, offset)
		offset = page.NextOffset
	}
	assert.Equal(t, string(contextJSON), strings.Join(chunks, ""))
	for _, offset := range []int{len(contextJSON), len(contextJSON) + 100} {
		result := callRetainedExecutionTool(t, read, map[string]any{"executionId": before.ID, "activityId": claim.Activity.ID, "part": "context", "offset": offset})
		require.False(t, result.IsError, "%s", result.Content)
		assert.Contains(t, result.Content, `"hasMore":false`)
		assert.Contains(t, result.Content, `"content":""`)
	}
	for _, change := range []map[string]any{{"offset": -1}, {"offset": strings.Index(string(contextJSON), "🦊") + 1}, {"offset": strings.Index(string(contextJSON), "🦊"), "limit": 1}, {"limit": -1}, {"limit": 4097}} {
		change["executionId"], change["part"] = before.ID, "context"
		change["activityId"] = claim.Activity.ID
		result := callRetainedExecutionTool(t, read, change)
		assert.True(t, result.IsError, "%s", result.Content)
	}
	result := callRetainedExecutionTool(t, read, map[string]any{"executionId": uuid.New(), "activityId": claim.Activity.ID, "part": "context"})
	assert.True(t, result.IsError, "an activity must belong to the selected execution")
	result = callRetainedExecutionTool(t, read, map[string]any{"executionId": before.ID, "activityId": uuid.New(), "part": "context"})
	assert.True(t, result.IsError)
	result = callRetainedExecutionTool(t, read, map[string]any{"executionId": before.ID, "part": "context"})
	require.False(t, result.IsError)
	var launch struct {
		Context struct {
			Content string `json:"content"`
		} `json:"context"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Content), &launch))
	assert.Equal(t, before.ContextJSON, launch.Context.Content)
	after, err := fixture.base.store.GetExecution(t.Context(), fixture.input.SessionID, before.ID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestExecutionToolsSurfaceStorageFailures(t *testing.T) {
	t.Parallel()
	for _, unavailable := range []bool{false, true} {
		t.Run(fmt.Sprint(unavailable), func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), false)
			if unavailable {
				fixture.base.runtime.store = struct{ session.Storage }{fixture.base.store}
			} else {
				require.NoError(t, fixture.base.handle.Close())
			}
			search := retainedExecutionTool(t, fixture, "searchExecutions")
			result := callRetainedExecutionTool(t, search, map[string]any{})
			assert.True(t, result.IsError)
			read := retainedExecutionTool(t, fixture, "readExecution")
			for _, part := range []string{"execution", "context", "activities", "events", "inbox"} {
				result := callRetainedExecutionTool(t, read, map[string]any{"executionId": fixture.input.ExecutionID, "part": part})
				assert.True(t, result.IsError, "%s: %s", part, result.Content)
			}
		})
	}
}

func TestExecutionToolsRejectInvalidReadsAndForeignIdentities(t *testing.T) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), false)
	search := retainedExecutionTool(t, fixture, "searchExecutions")
	read := retainedExecutionTool(t, fixture, "readExecution")
	testCases := []struct {
		name, tool string
		arguments  map[string]any
	}{
		{name: "missing identity", tool: "read"},
		{name: "unknown identity", tool: "read", arguments: map[string]any{"executionId": uuid.New()}},
		{name: "unknown part", tool: "read", arguments: map[string]any{"executionId": fixture.input.ExecutionID, "part": "invalid"}},
		{name: "negative offset", tool: "read", arguments: map[string]any{"executionId": fixture.input.ExecutionID, "part": "events", "offset": -1}},
		{name: "oversized page", tool: "read", arguments: map[string]any{"executionId": fixture.input.ExecutionID, "part": "activities", "limit": session.MaximumPageLimit + 1}},
		{name: "unknown state", tool: "search", arguments: map[string]any{"state": "invalid"}},
		{name: "bad time", tool: "search", arguments: map[string]any{"fromTime": "later"}},
		{name: "unknown field", tool: "search", arguments: map[string]any{"sessionId": uuid.New()}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tool := read
			if tc.tool == "search" {
				tool = search
			}
			arguments := tc.arguments
			if arguments == nil {
				arguments = map[string]any{}
			}
			result := callRetainedExecutionTool(t, tool, arguments)
			assert.True(t, result.IsError, "%s", result.Content)
			assert.NotContains(t, result.Content, " [/work/")
		})
	}
	t.Run("foreign session", func(t *testing.T) {
		otherSession, _ := inboxSession(t, fixture.base)
		deps := &launchAgentDeps{runtime: fixture.base.runtime, sessionID: otherSession}
		for _, tool := range messageTools(deps, nil) {
			if tool.Name != "readExecution" {
				continue
			}
			result := callRetainedExecutionTool(t, tool, map[string]any{"executionId": fixture.input.ExecutionID})
			assert.True(t, result.IsError)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := search.Handler(ctx, elelem.ToolInput{Arguments: json.RawMessage(`{}`)})
		require.ErrorIs(t, err, context.Canceled)
	})
}
