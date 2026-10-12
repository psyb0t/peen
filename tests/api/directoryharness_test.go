//go:build integration

package api_test

import (
	"testing"

	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	directoryHopMessage = "enter nested and read the fixture"
	directoryHopMarker  = "Directory-specific worker instructions."
	directoryHopContent = "The nested file was read."
)

func TestAPIDirectoryHopReloadsWorkerHarnessAndPersistsCWD(t *testing.T) {
	workspace := openFreshWorkspace(t, "directory-", map[string]string{
		"nested/AGENTS.md": directoryHopMarker,
		"nested/local.txt": directoryHopContent,
	})
	connection := openSessionConnection(t, workspace.sessionID)
	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: directoryHopMessage,
		Calls: []testinfra.ScriptedToolCall{
			{Name: "changeDirectory", Arguments: map[string]any{"directory": "nested"}},
			{Name: "readFile", Arguments: map[string]any{"path": "local.txt"}},
		},
		FinalAnswer: "done",
	})
	before := len(integrationInfra.ProviderRequests())
	sendSessionMessage(t, connection, workspace.sessionID, directoryHopMessage)
	readFramesUntilTurnCompleted(t, connection)
	requests := providerRequestsSince(before)
	require.Len(t, requests, 3)
	assert.NotContains(t, requests[0][0].Content, directoryHopMarker)
	assert.Contains(t, requests[1][0].Content, directoryHopMarker)
	assert.Contains(t, requests[2][0].Content, directoryHopMarker)
	messages := storedMessages(t, workspace.sessionID)
	var toolResults int
	var readSeen bool
	for _, message := range messages {
		if message.Role != api.MessageRoleTool {
			continue
		}
		toolResults++
		assert.False(t, message.IsError != nil && *message.IsError)
		if message.ToolCallId != nil && *message.ToolCallId == testinfra.ScriptedCallID(1) {
			readSeen = true
			assert.Contains(t, message.Content, directoryHopContent)
		}
	}
	assert.Equal(t, 2, toolResults)
	assert.True(t, readSeen)
	integrationInfra.DisableScriptedToolTurn()
	before = len(integrationInfra.ProviderRequests())
	sendSessionMessage(t, connection, workspace.sessionID, "continue in the same directory")
	readFramesUntilTurnCompleted(t, connection)
	requests = providerRequestsSince(before)
	require.Len(t, requests, 1)
	assert.Contains(t, requests[0][0].Content, directoryHopMarker)
}
