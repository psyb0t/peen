//go:build integration

package api_test

import (
	"testing"

	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	commandEnvWorkspacePrefix = "commandenv-"
	commandEnvMessage         = "print your environment"
	commandEnvFinalAnswer     = "the environment was printed"
	commandEnvCommand         = "env"
	commandEnvPurpose         = "api command environment fixture"
)

// The control worker needs the provider credentials to call models, but a
// command the agent runs must not inherit the controller's API token or any
// provider key. A single `env` call would otherwise write them into the stored
// transcript and the live feed.
func TestAPICommandsDoNotInheritControllerSecrets(t *testing.T) {
	workspace := openFreshWorkspace(t, commandEnvWorkspacePrefix, nil)
	connection := openSessionConnection(t, workspace.sessionID)

	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: commandEnvMessage,
		Calls: []testinfra.ScriptedToolCall{{
			Name: "runCommand",
			Arguments: map[string]any{
				"command": commandEnvCommand,
				"purpose": commandEnvPurpose,
			},
		}},
		FinalAnswer: commandEnvFinalAnswer,
	})

	sendSessionMessage(t, connection, workspace.sessionID, commandEnvMessage)
	readFramesUntilTurnCompleted(t, connection)

	tools := toolMessages(t, workspace.sessionID)
	require.Len(t, tools, 1)
	require.False(t, toolFailed(tools[0]))

	output := tools[0].Content

	assert.Contains(t, output, "PATH=", "the command ran with a usable environment")
	assert.NotContains(t, output, testinfra.TestAPIToken)
	assert.NotContains(t, output, "PEEN_API_TOKEN=")
	assert.NotContains(t, output, testinfra.TestProviderKey)
	assert.NotContains(t, output, testinfra.ProviderKeyEnvName+"=")
}
