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

	procEnvWorkspacePrefix = "procenv-"
	procEnvMessage         = "read the process environments"
	procEnvFinalAnswer     = "the process environments were read"
	procEnvPurpose         = "api proc environment fixture"
	procEnvDenied          = "Permission denied"

	// procEnvCommand reads the environment of the command's parent, the
	// session worker, and then of every process it can see, the controller
	// included. Unreadable files are skipped, so the walk prints only what a
	// same-user process can read, its own environment among them.
	procEnvCommand = `tr '\0' '\n' < "/proc/$PPID/environ" 2>&1
for f in /proc/[0-9]*/environ; do tr '\0' '\n' < "$f" 2>/dev/null; done
true`
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

// A control agent runs as the controller's user, so Linux would let its
// commands read /proc/<pid>/environ of the controller and of the session
// worker. Neither may hand over the API token or the provider key that way.
func TestAPIControlAgentCannotReadSecretsThroughProc(t *testing.T) {
	workspace := openFreshWorkspace(t, procEnvWorkspacePrefix, nil)
	connection := openSessionConnection(t, workspace.sessionID)

	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: procEnvMessage,
		Calls: []testinfra.ScriptedToolCall{{
			Name: "runCommand",
			Arguments: map[string]any{
				"command": procEnvCommand,
				"purpose": procEnvPurpose,
			},
		}},
		FinalAnswer: procEnvFinalAnswer,
	})

	sendSessionMessage(t, connection, workspace.sessionID, procEnvMessage)
	readFramesUntilTurnCompleted(t, connection)

	tools := toolMessages(t, workspace.sessionID)
	require.Len(t, tools, 1)
	require.False(t, toolFailed(tools[0]))

	output := tools[0].Content

	assert.Contains(t, output, "PATH=", "the walk read at least its own environment")
	assert.Contains(t, output, procEnvDenied, "the worker's environment is closed")
	assert.NotContains(t, output, testinfra.TestAPIToken)
	assert.NotContains(t, output, testinfra.TestProviderKey)
}
