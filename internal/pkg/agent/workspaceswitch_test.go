package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	workspaceSwitchFileName  = "which-workspace.txt"
	workspaceSwitchFirstBody = "first workspace"
	workspaceSwitchOtherBody = "other workspace"
	workspaceSwitchCallID    = "workspace_switch_call"
	workspaceSwitchFinalText = "read it"
)

// A per-message workspace must move the TOOLS, not just the rules.
//
// Rule layering is already proven elsewhere, and it is the easier half: the
// prompt is rebuilt every turn either way. The half that can rot silently is
// the executor, which is built once per turn from the resolved workspace. If a
// resumed turn kept the first turn's executor, every relative path in the
// second turn would resolve against the previous tree, and a write would land
// in the wrong repository with nothing in the transcript to show it.
//
// Both workspaces hold the same filename with different contents, so the tool
// result itself says which directory actually served the call.
func TestResumedTurnRunsToolsInTheNewWorkspace(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("first reply"),
		elelemtest.ToolCall(
			workspaceSwitchCallID,
			toolNameReadFile,
			runtimeToolArguments(t, workspaceSwitchFileName),
		),
		elelemtest.Text(workspaceSwitchFinalText),
	))

	writeRuntimeFile(
		t,
		filepath.Join(fixture.workspace, workspaceSwitchFileName),
		workspaceSwitchFirstBody,
	)
	writeRuntimeFile(
		t,
		filepath.Join(fixture.otherWorkspace, workspaceSwitchFileName),
		workspaceSwitchOtherBody,
	)

	first, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "open the session here",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	events := make([]Event, 0)

	second, err := fixture.runtime.Run(context.Background(), TurnRequest{
		SessionID: &first.SessionID,
		Message:   "now read it over there",
		Workspace: fixture.otherWorkspace,
		RequestID: uuid.New(),
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err)
	assert.Equal(t, first.SessionID, second.SessionID)

	result := toolResultPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolResult),
		&result,
	))
	require.False(t, result.IsError, "the read must succeed: %s", result.Content)

	assert.Contains(
		t,
		result.Content,
		workspaceSwitchOtherBody,
		"the tool ran against the previous turn's workspace",
	)
	assert.NotContains(t, result.Content, workspaceSwitchFirstBody)
}

// The reverse direction: omitting the workspace on a later turn falls back to
// the runtime default rather than inheriting whatever the previous turn used.
// A per-message value that quietly became session state would be sticky, which
// the contract forbids.
func TestOmittedWorkspaceDoesNotInheritThePreviousTurn(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("first reply"),
		elelemtest.ToolCall(
			workspaceSwitchCallID,
			toolNameReadFile,
			runtimeToolArguments(t, workspaceSwitchFileName),
		),
		elelemtest.Text(workspaceSwitchFinalText),
	))

	// Only the default workspace holds the file. If the second turn inherited
	// the first turn's explicit workspace, the read would fail outright.
	writeRuntimeFile(
		t,
		filepath.Join(fixture.workspace, workspaceSwitchFileName),
		workspaceSwitchFirstBody,
	)

	first, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "open the session over there",
		Workspace: fixture.otherWorkspace,
	})
	require.NoError(t, err)

	events := make([]Event, 0)

	_, err = fixture.runtime.Run(context.Background(), TurnRequest{
		SessionID: &first.SessionID,
		Message:   "read it without naming a workspace",
		RequestID: uuid.New(),
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err)

	result := toolResultPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolResult),
		&result,
	))
	require.False(t, result.IsError, "the read must succeed: %s", result.Content)
	assert.Contains(t, result.Content, workspaceSwitchFirstBody)
}
