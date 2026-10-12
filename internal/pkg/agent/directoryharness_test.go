package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const directoryPublicationCheckDuration = 25 * time.Millisecond

func TestDirectoryHopQueuedMessagesKeepCurrentDirectory(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("hop", toolNameChangeDirectory, `{"directory":"nested"}`),
		elelemtest.Text("done"),
	)
	fixture := newRuntimeFixture(t, driver)
	nested := filepath.Join(fixture.workspace, "nested")
	writeRuntimeFile(t, filepath.Join(nested, ".agents", "skills", "local", "SKILL.md"), "---\nname: local\ndescription: nested skill\n---\nNested skill instructions.")
	ctx := context.Background()
	var queued bool
	result, err := fixture.runtime.Run(ctx, TurnRequest{
		Message: "hop",
		OnEvent: func(event Event) error {
			if event.Type != EventTypeToolResult {
				return nil
			}
			result, err := fixture.runtime.Run(ctx, TurnRequest{Message: ":local"})
			if err != nil {
				return err
			}
			queued = result.Queued

			return nil
		},
	})
	require.NoError(t, err)
	assert.True(t, queued)
	page, err := fixture.store.ListMessages(ctx, result.SessionID, session.ListMessagesOptions{})
	require.NoError(t, err)
	var delivered, skill bool
	for _, message := range page.Items {
		if message.Content == ":local" {
			delivered = true
			assert.Equal(t, nested, message.Workspace)
		}
		if message.InjectedKind == models.InjectedKindSkill {
			skill = true
			assert.Equal(t, nested, message.Workspace)
		}
	}
	assert.True(t, delivered)
	assert.True(t, skill)
}

func TestDirectoryHopPostHookDenialKeepsScopeAcrossTurns(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("hop", toolNameChangeDirectory, `{"directory":"nested"}`),
		elelemtest.Text("denied"),
		elelemtest.Text("next turn"),
	)
	fixture := newRuntimeFixture(t, driver)
	writeRuntimeFile(t, filepath.Join(fixture.workspace, "nested", "AGENTS.md"), "rejected-scope")
	writeRuntimeFile(t, filepath.Join(fixture.workspace, ".agents", "hooks.yaml"), `version: 1
postToolUse:
  - name: denyHopResult
    match:
      tool: changeDirectory
    actions:
      - name: deny
        type: deny
        reason: rejected hop result
`)
	ctx := context.Background()
	result, err := fixture.runtime.Run(ctx, TurnRequest{Message: "hop"})
	require.NoError(t, err)
	_, err = fixture.runtime.Run(ctx, TurnRequest{Message: "continue"})
	require.NoError(t, err)
	requests := driver.Requests()
	require.Len(t, requests, 3)
	for _, request := range requests {
		assert.NotContains(t, requestSystemPrompt(request), "rejected-scope")
	}
	directory, err := fixture.runtime.currentDirectory(ctx, result.SessionID, fixture.workspace)
	require.NoError(t, err)
	assert.Equal(t, fixture.workspace, directory)
	page, err := fixture.store.ListMessages(ctx, result.SessionID, session.ListMessagesOptions{})
	require.NoError(t, err)
	var failedHop bool
	for _, message := range page.Items {
		if message.Role == models.MessageRoleTool && message.ToolCallID == "hop" {
			failedHop = message.IsError
		}
	}
	assert.True(t, failedHop)
}

func TestDirectoryPublicationWaitsForQueuedSkillLookup(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		cancelled bool
	}{
		{name: "publish"},
		{name: "cancel before publication", cancelled: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			queue := &activeUserMessageQueue{skills: newSkillLedger()}
			original := &directoryContext{skills: queue.skills}
			candidate := &directoryContext{skills: newSkillLedger()}
			scope := &directoryScope{options: directoryScopeOptions{queue: queue}, current: original}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			queue.mutex.Lock()
			done := make(chan error, 1)
			go func() { done <- scope.publishContext(ctx, candidate) }()
			assert.Never(t, func() bool { return scope.context() == candidate }, directoryPublicationCheckDuration, time.Millisecond)
			if tc.cancelled {
				cancel()
			}
			queue.mutex.Unlock()
			err := <-done
			if tc.cancelled {
				require.ErrorIs(t, err, context.Canceled)
				assert.Same(t, original, scope.context())
				assert.Same(t, original.skills, queue.skills)

				return
			}
			require.NoError(t, err)
			assert.Same(t, candidate, scope.context())
			assert.Same(t, candidate.skills, queue.skills)
		})
	}
}

type directorySnapshotFailure struct {
	session.Storage
	prompt       bool
	contextCalls int
	promptCalls  int
}

func (s *directorySnapshotFailure) SaveContextSnapshot(
	ctx context.Context,
	snapshot *models.ContextSnapshot,
) error {
	s.contextCalls++
	if !s.prompt && s.contextCalls > 1 {
		return commerr.ErrPermissionDenied
	}

	return s.Storage.SaveContextSnapshot(ctx, snapshot)
}

func (s *directorySnapshotFailure) SavePromptSnapshot(
	ctx context.Context,
	snapshot *models.PromptSnapshot,
) error {
	s.promptCalls++
	if s.prompt && s.promptCalls > 1 {
		return commerr.ErrPermissionDenied
	}

	return s.Storage.SavePromptSnapshot(ctx, snapshot)
}

func TestDirectoryHopSnapshotFailuresLeaveThePreviousScopeActive(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		prompt bool
	}{
		{name: "context snapshot"},
		{name: "prompt snapshot", prompt: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			driver := elelemtest.NewScriptedDriver(elelemtest.ToolCall("hop", toolNameChangeDirectory, `{"directory":"nested"}`), elelemtest.Text("done"))
			fixture := newRuntimeFixture(t, driver)
			writeRuntimeFile(t, filepath.Join(fixture.workspace, "nested", "AGENTS.md"), "unpublished candidate")
			fixture.runtime.store = &directorySnapshotFailure{Storage: fixture.store, prompt: tc.prompt}
			result, err := fixture.runtime.Run(context.Background(), TurnRequest{Message: "try hop"})
			require.NoError(t, err)
			requests := driver.Requests()
			require.Len(t, requests, 2)
			assert.Equal(t, requestSystemPrompt(requests[0]), requestSystemPrompt(requests[1]))
			directory, err := fixture.runtime.currentDirectory(context.Background(), result.SessionID, fixture.workspace)
			require.NoError(t, err)
			assert.Equal(t, fixture.workspace, directory)
		})
	}
}

func TestDirectoryHopReplacesPromptBeforeNextProviderCall(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("hop", "changeDirectory", `{"directory":"nested"}`),
		elelemtest.Text("done"),
	)
	fixture := newRuntimeFixture(t, driver)
	writeRuntimeFile(t, filepath.Join(fixture.workspace, "nested", "AGENTS.md"), "nested-scope-marker")
	_, err := fixture.runtime.Run(context.Background(), TurnRequest{Message: "hop", Workspace: fixture.workspace})
	require.NoError(t, err)
	requests := driver.Requests()
	require.Len(t, requests, 2)
	assert.NotContains(t, requests[0].Messages[0].Text(), "nested-scope-marker")
	assert.Contains(t, requests[1].Messages[0].Text(), "nested-scope-marker")
	assert.Equal(t, elelem.RoleSystem, requests[1].Messages[0].Role)
}

func TestDirectoryHopReportsInvalidOptionalTargetFiles(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.ToolCall("hop", toolNameChangeDirectory, `{"directory":"nested"}`), elelemtest.Text("done"))
	fixture := newRuntimeFixture(t, driver)
	writeRuntimeFile(t, filepath.Join(fixture.workspace, "nested", ".agents", "rules", "empty.md"), "")
	var warnings []harnessWarningPayload
	_, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message: "hop",
		OnEvent: func(event Event) error {
			if event.Type != EventTypeHarnessWarning {
				return nil
			}
			var payload harnessWarningPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return err
			}
			warnings = append(warnings, payload)

			return nil
		},
	})
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	require.Len(t, warnings[0].Warnings, 1)
	assert.Contains(t, warnings[0].Warnings[0].Source, "empty.md")
	requests := driver.Requests()
	require.Len(t, requests, 2)
	assert.Contains(t, requestSystemPrompt(requests[1]), "empty.md")
}

func TestDirectoryHopReplacesRootToolAllowlistAndEventHandlers(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("hop", toolNameChangeDirectory, `{"directory":"nested"}`),
		elelemtest.Text("done"),
	)
	fixture := newRuntimeFixture(t, driver)
	writeRuntimeFile(t, filepath.Join(fixture.workspace, "nested", ".agents", "agents", "default.md"), `---
name: default
description: nested root
allowed-tools: readFile, changeDirectory
---
Nested root instructions.`)
	writeRuntimeFile(t, filepath.Join(fixture.workspace, "nested", ".agents", "events", "scoped.notice.md"), `---
type: scoped.notice
delivery: queue
---
Handle the nested notice.`)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{Message: "hop"})
	require.NoError(t, err)
	requests := driver.Requests()
	require.Len(t, requests, 2)
	names := make([]string, 0, len(requests[1].Tools))
	for _, tool := range requests[1].Tools {
		names = append(names, tool.Name)
	}
	assert.Contains(t, names, toolNameReadFile)
	assert.Contains(t, names, toolNameChangeDirectory)
	assert.NotContains(t, names, toolNameRunCommand)
	assert.NotContains(t, names, toolNameUseSkill)
	assert.Contains(t, requestSystemPrompt(requests[1]), "Nested root instructions.")
	_, found, err := fixture.runtime.wakeHandler(context.Background(), events.Notice{SessionID: result.SessionID, Type: "scoped.notice"})
	require.NoError(t, err)
	assert.True(t, found)
}

func TestDirectoryHopRejectsAnInvalidCandidateWithoutPartialSwitch(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		limit bool
	}{
		{name: "unknown allowed tool"},
		{name: "instruction limit", limit: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			driver := elelemtest.NewScriptedDriver(elelemtest.ToolCall("hop", toolNameChangeDirectory, `{"directory":"nested"}`), elelemtest.Text("done"))
			fixture := newRuntimeFixtureWithOptions(t, driver, func(options *RuntimeOptions) {
				if tc.limit {
					resolver, err := harness.NewResolver(options.ConfigDirectory, harness.Limits{MaxInstructions: 2})
					require.NoError(t, err)
					options.Resolver = resolver
				}
			})
			writeRuntimeFile(t, filepath.Join(fixture.workspace, "nested", "AGENTS.md"), "must-not-enter-scope")
			if !tc.limit {
				writeRuntimeFile(t, filepath.Join(fixture.workspace, "nested", ".agents", "agents", "default.md"), "---\nname: default\ndescription: invalid allowlist\nallowed-tools: missingTool\n---\nInvalid target.")
			}
			result, err := fixture.runtime.Run(context.Background(), TurnRequest{Message: "try hop"})
			require.NoError(t, err)
			requests := driver.Requests()
			require.Len(t, requests, 2)
			assert.Equal(t, requestSystemPrompt(requests[0]), requestSystemPrompt(requests[1]))
			directory, err := fixture.runtime.currentDirectory(context.Background(), result.SessionID, fixture.workspace)
			require.NoError(t, err)
			assert.Equal(t, fixture.workspace, directory)
		})
	}
}

func TestDirectoryHopHonorsTheCurrentScopesPathDenial(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.ToolCall("hop", toolNameChangeDirectory, `{"directory":"nested"}`), elelemtest.Text("done"))
	fixture := newRuntimeFixture(t, driver)
	writeRuntimeFile(t, filepath.Join(fixture.workspace, "nested", "AGENTS.md"), "denied target")
	writeRuntimeFile(t, filepath.Join(fixture.workspace, ".agents", "hooks.yaml"), `version: 1
preToolUse:
  - name: denyHop
    match:
      tool: changeDirectory
      path: nested
    actions:
      - name: deny
        type: deny
        reason: hop denied by path policy
`)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{Message: "try hop"})
	require.NoError(t, err)
	requests := driver.Requests()
	require.Len(t, requests, 2)
	assert.NotContains(t, requestSystemPrompt(requests[1]), "denied target")
	assert.Contains(t, conversationText(requests[1:]), "hop denied by path policy")
	directory, err := fixture.runtime.currentDirectory(context.Background(), result.SessionID, fixture.workspace)
	require.NoError(t, err)
	assert.Equal(t, fixture.workspace, directory)
}

func TestDirectoryHopPersistsAcrossTurnsWithTruncatedOutput(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("hop", toolNameChangeDirectory, `{"directory":"nested"}`),
		elelemtest.Text("first done"),
		elelemtest.ToolCall("read", toolNameReadFile, `{"path":"file.txt"}`),
		elelemtest.Text("second done"),
	)
	fixture := newRuntimeFixtureWithOptions(t, driver, func(options *RuntimeOptions) {
		options.MaxToolResultTokens = 1
	})
	nested := filepath.Join(fixture.workspace, "nested")
	writeRuntimeFile(t, filepath.Join(nested, "AGENTS.md"), "persisted scope")
	writeRuntimeFile(t, filepath.Join(nested, "file.txt"), "from nested directory")
	ctx := context.Background()
	first, err := fixture.runtime.Run(ctx, TurnRequest{Message: "hop"})
	require.NoError(t, err)
	second, err := fixture.runtime.Run(ctx, TurnRequest{Message: "read"})
	require.NoError(t, err)
	assert.Equal(t, first.SessionID, second.SessionID)
	stored, err := fixture.store.Get(ctx, first.SessionID)
	require.NoError(t, err)
	assert.Equal(t, fixture.workspace, stored.Workspace)
	requests := driver.Requests()
	require.Len(t, requests, 4)
	assert.Contains(t, requestSystemPrompt(requests[2]), "persisted scope")
	page, err := fixture.store.ListMessages(ctx, first.SessionID, session.ListMessagesOptions{})
	require.NoError(t, err)
	for _, message := range page.Items {
		if message.ToolCallID == "hop" {
			assert.Equal(t, nested, message.Workspace)
			assert.False(t, message.IsError)
		}
		if message.ToolCallID == "read" {
			assert.False(t, message.IsError)
		}
	}
}

func TestDirectoryHopReplacesSkillsAndHooksThenDropsSiblingScope(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("a", toolNameChangeDirectory, `{"directory":"a"}`),
		elelemtest.ToolCall("skill-a", toolNameUseSkill, `{"name":"local"}`),
		elelemtest.ToolCall("b", toolNameChangeDirectory, `{"directory":"../b"}`),
		elelemtest.ToolCall("skill-b", toolNameUseSkill, `{"name":"local"}`),
		elelemtest.ToolCall("denied", toolNameMakeDirectory, `{"path":"blocked"}`),
		elelemtest.ToolCall("parent", toolNameChangeDirectory, `{"directory":".."}`),
		elelemtest.Text("done"),
	)
	fixture := newRuntimeFixture(t, driver)
	for _, directory := range []string{"a", "b"} {
		writeRuntimeFile(t, filepath.Join(fixture.workspace, directory, "AGENTS.md"), "scope-"+directory)
		writeRuntimeFile(t, filepath.Join(fixture.workspace, directory, ".agents", "skills", "local", "SKILL.md"),
			"---\nname: local\ndescription: local task\n---\nskill-"+directory)
	}
	writeRuntimeFile(t, filepath.Join(fixture.workspace, "b", ".agents", "hooks.yaml"), `version: 1
preToolUse:
  - name: refuseDirectory
    match:
      tool: makeDirectory
    actions:
      - name: deny
        type: deny
        reason: scope-b-denied
`)
	_, err := fixture.runtime.Run(context.Background(), TurnRequest{Message: "visit siblings"})
	require.NoError(t, err)
	requests := driver.Requests()
	require.Len(t, requests, 7)
	assert.Contains(t, requestSystemPrompt(requests[1]), "scope-a")
	assert.Contains(t, requestSystemPrompt(requests[3]), "scope-b")
	assert.NotContains(t, requestSystemPrompt(requests[3]), "scope-a")
	assert.NotContains(t, requestSystemPrompt(requests[6]), "scope-b")
	assert.Contains(t, conversationText(requests[2:3]), "skill-a")
	assert.Contains(t, conversationText(requests[4:5]), "skill-b")
	assert.Contains(t, conversationText(requests[5:6]), "scope-b-denied")
}

func TestDirectoryHopKeepsRoundBudgetAndAuditIndexes(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("a", toolNameChangeDirectory, `{"directory":"nested"}`).WithUsage(elelem.Usage{TokenCounts: elelem.TokenCounts{Prompt: 10, Completion: 2, Total: 12}}),
		elelemtest.ToolCall("b", toolNameChangeDirectory, `{"directory":".."}`).WithUsage(elelem.Usage{TokenCounts: elelem.TokenCounts{Prompt: 20, Completion: 3, Total: 23}}),
		elelemtest.Text("must not run"),
	)
	fixture := newRuntimeFixtureWithOptions(t, driver, func(options *RuntimeOptions) {
		options.MaxToolRounds = 2
	})
	writeRuntimeFile(t, filepath.Join(fixture.workspace, "nested", "AGENTS.md"), "nested")
	_, err := fixture.runtime.Run(context.Background(), TurnRequest{Message: "loop"})
	require.ErrorIs(t, err, elelem.ErrMaxRoundsExceeded)
	require.Len(t, driver.Requests(), 2)
	opened, err := fixture.store.OpenWorkspace(context.Background(), fixture.workspace, session.OpenSessionOptions{})
	require.NoError(t, err)
	runs, err := fixture.store.ListModelRuns(context.Background(), opened.Session.ID, session.ListModelRunsOptions{})
	require.NoError(t, err)
	require.Len(t, runs.Items, 1)
	calls, err := fixture.store.ListModelCalls(context.Background(), opened.Session.ID, runs.Items[0].ID, session.ListModelCallsOptions{})
	require.NoError(t, err)
	require.Len(t, calls.Items, 2)
	assert.Equal(t, []int64{0, 1}, []int64{calls.Items[0].Round, calls.Items[1].Round})
}

func TestDirectoryHopFailedTargetKeepsPromptAndDirectory(t *testing.T) {
	t.Parallel()
	testCases := []struct{ name, arguments string }{
		{name: "empty", arguments: `{"directory":""}`},
		{name: "missing", arguments: `{"directory":"missing"}`},
		{name: "file", arguments: `{"directory":"file.txt"}`},
		{name: "wrong type", arguments: `{"directory":42}`},
		{name: "null", arguments: `{"directory":null}`},
		{name: "unknown field", arguments: `{"directory":".","extra":true}`},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			driver := elelemtest.NewScriptedDriver(elelemtest.ToolCall("hop", toolNameChangeDirectory, tc.arguments), elelemtest.Text("done"))
			fixture := newRuntimeFixture(t, driver)
			writeRuntimeFile(t, filepath.Join(fixture.workspace, "file.txt"), "file")
			result, err := fixture.runtime.Run(context.Background(), TurnRequest{Message: "try a hop"})
			require.NoError(t, err)
			requests := driver.Requests()
			require.Len(t, requests, 2)
			assert.Equal(t, requestSystemPrompt(requests[0]), requestSystemPrompt(requests[1]))
			page, err := fixture.store.ListMessages(context.Background(), result.SessionID, session.ListMessagesOptions{})
			require.NoError(t, err)
			for _, message := range page.Items {
				if message.Role == models.MessageRoleTool {
					assert.True(t, message.IsError)
				}
			}
		})
	}
}

func TestDirectoryHopInChildLeavesParentScopeAlone(t *testing.T) {
	t.Parallel()
	driver := launchChildDriver(t,
		elelemtest.ToolCall("hop", toolNameChangeDirectory, `{"directory":"nested"}`),
		elelemtest.ToolCall("read", toolNameReadFile, `{"path":"file.txt"}`),
		elelemtest.Text("child done"),
	)
	fixture := childHarnessFixture(t, driver)
	writeRuntimeFile(t, filepath.Join(fixture.workspace, "nested", "AGENTS.md"), "child-only-scope")
	writeRuntimeFile(t, filepath.Join(fixture.workspace, "nested", "file.txt"), "child file")
	result := runChildLaunchTurn(t, fixture)
	child, root := splitChildRequests(t, driver)
	require.Len(t, child, 3)
	assert.Contains(t, requestSystemPrompt(child[1]), "child-only-scope")
	for _, request := range root {
		assert.NotContains(t, requestSystemPrompt(request), "child-only-scope")
	}
	directory, err := fixture.runtime.currentDirectory(context.Background(), result.SessionID, fixture.workspace)
	require.NoError(t, err)
	assert.Equal(t, fixture.workspace, directory)
}

func directoryToolCalls(t *testing.T, calls ...elelem.ToolCall) string {
	t.Helper()
	data, err := json.Marshal(calls)
	require.NoError(t, err)

	return string(data)
}
