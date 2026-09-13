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
	workspaceSwitchFirstBody = "startup workspace"
	workspaceSwitchOtherBody = "other workspace"
	workspaceSwitchCallID    = "workspace_switch_call"
	workspaceSwitchFinalText = "read it"
)

func TestRuntimeKeepsToolsInTheStartupWorkspace(t *testing.T) {
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
		Message: "open the startup workspace session",
	})
	require.NoError(t, err)

	events := make([]Event, 0)

	second, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "try to read another workspace",
		Workspace: fixture.otherWorkspace,
		RequestID: uuid.New(),
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err)
	assert.Equal(t, fixture.runtime.SessionID(), first.SessionID)
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
		workspaceSwitchFirstBody,
	)
	assert.NotContains(t, result.Content, workspaceSwitchOtherBody)
}
