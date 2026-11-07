//go:build integration

package executionforms

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutionFormsExposeTheRestartSafeHTTPContract(t *testing.T) {
	requireNoDockerDaemon(t)

	repository := executionFormsRepositoryRoot(t)
	forms := []executionForm{
		{
			name:  executionFormsSourceForm,
			build: buildSourceForm,
		},
		{
			name:  executionFormsInstalledForm,
			build: buildInstalledForm,
		},
	}

	for _, form := range forms {
		t.Run(form.name, func(t *testing.T) {
			binary := form.build(t, repository)
			runAuthenticatedFormScenario(t, binary)
			runUnauthenticatedFormScenario(t, binary)
		})
	}
}

func TestExecutionFormsServeATurnWithTheDefaultAgent(t *testing.T) {
	requireNoDockerDaemon(t)

	binary := buildSourceForm(t, executionFormsRepositoryRoot(t))
	provider := testinfra.NewProviderMock()
	t.Cleanup(provider.Close)
	state := newProcessState(t)
	removeCustomDefaultAgent(t, state)

	process := startPeen(t, processConfig{
		binary:          binary,
		configDirectory: state.configDirectory,
		stateDirectory:  state.stateDirectory,
		workspace:       state.workspace,
		workerSocketDir: state.workerSocketDir,
		apiAddress:      reserveLoopbackAddress(t),
		metricsAddress:  reserveLoopbackAddress(t),
		provider:        provider,
		apiToken:        executionFormsAPIToken,
		unsetAgent:      true,
	})
	t.Cleanup(func() { process.stop(t) })

	sendWebSocketTurn(
		t,
		process.baseURL,
		executionFormsAPIToken,
		state.workspace,
		executionFormsAgentUnsetMessage,
	)

	assert.Contains(
		t,
		provider.LastSystemPrompt(),
		executionFormsGeneralPurposeInstructions,
	)
}

// removeCustomDefaultAgent deletes the config directory's custom default
// agent, so "default" has no definition and core:general-purpose is the only
// embedded agent.
func removeCustomDefaultAgent(t *testing.T, state processState) {
	t.Helper()

	require.NoError(t, os.Remove(filepath.Join(
		state.configDirectory,
		executionFormsAgentsDirectory,
		executionFormsAgentFile,
	)))
}

func runAuthenticatedFormScenario(t *testing.T, binary string) {
	t.Helper()

	provider := testinfra.NewProviderMock()
	t.Cleanup(provider.Close)
	state := newProcessState(t)
	process := startPeen(t, processConfig{
		binary:          binary,
		configDirectory: state.configDirectory,
		stateDirectory:  state.stateDirectory,
		workspace:       state.workspace,
		workerSocketDir: state.workerSocketDir,
		apiAddress:      reserveLoopbackAddress(t),
		metricsAddress:  reserveLoopbackAddress(t),
		provider:        provider,
		apiToken:        executionFormsAPIToken,
	})
	t.Cleanup(func() { process.stop(t) })

	assertUnauthorized(t, process.baseURL)

	provider.EnableScriptedToolTurn(jsonToolTurn())
	jsonSessionID := sendWebSocketTurn(
		t,
		process.baseURL,
		executionFormsAPIToken,
		state.workspace,
		executionFormsJSONToolMessage,
	)
	provider.DisableScriptedToolTurn()
	assertWorkspaceFile(t, state.workspace, executionFormsJSONFixtureFile, executionFormsJSONAfter)
	assertWorkspaceFile(t, state.workspace, executionFormsJSONCommandFile, executionFormsJSONOutput)

	provider.EnableScriptedToolTurn(patchToolTurn())
	sendWebSocketTurn(
		t,
		process.baseURL,
		executionFormsAPIToken,
		state.workspace,
		executionFormsPatchToolMessage,
	)
	provider.DisableScriptedToolTurn()
	assertWorkspaceFile(t, state.workspace, executionFormsPatchFixtureFile, executionFormsPatchAfter)
	assertWorkspaceFile(t, state.workspace, executionFormsPatchCommandFile, executionFormsPatchOutput)

	process.stop(t)
	process = startPeen(t, processConfig{
		binary:          binary,
		configDirectory: state.configDirectory,
		stateDirectory:  state.stateDirectory,
		workspace:       state.workspace,
		workerSocketDir: state.workerSocketDir,
		apiAddress:      reserveLoopbackAddress(t),
		metricsAddress:  reserveLoopbackAddress(t),
		provider:        provider,
		apiToken:        executionFormsAPIToken,
	})

	assertSessionHistory(t, process.baseURL, executionFormsAPIToken, jsonSessionID)
	writeFile(
		t,
		filepath.Join(state.workspace, executionFormsRulesFile),
		executionFormsUpdatedRules,
	)
	sendWebSocketTurn(
		t,
		process.baseURL,
		executionFormsAPIToken,
		state.workspace,
		executionFormsReloadMessage,
	)
	assert.Contains(t, provider.LastSystemPrompt(), executionFormsUpdatedRules)
	assertPrivateMetrics(t, process)

	// Durable state lives in the state directory, never in the configuration
	// directory a Docker worker receives read-only.
	assert.FileExists(
		t,
		filepath.Join(state.stateDirectory, executionFormsDatabaseFile),
	)
	assert.NoFileExists(
		t,
		filepath.Join(state.configDirectory, executionFormsDatabaseFile),
	)
}

func runUnauthenticatedFormScenario(t *testing.T, binary string) {
	t.Helper()

	provider := testinfra.NewProviderMock()
	t.Cleanup(provider.Close)
	state := newProcessState(t)
	process := startPeen(t, processConfig{
		binary:          binary,
		configDirectory: state.configDirectory,
		stateDirectory:  state.stateDirectory,
		workspace:       state.workspace,
		workerSocketDir: state.workerSocketDir,
		apiAddress:      reserveLoopbackAddress(t),
		metricsAddress:  reserveLoopbackAddress(t),
		provider:        provider,
	})
	t.Cleanup(func() { process.stop(t) })

	sendWebSocketTurn(
		t,
		process.baseURL,
		"",
		state.workspace,
		executionFormsUnauthMessage,
	)
}

func jsonToolTurn() testinfra.ScriptedToolTurn {
	return testinfra.ScriptedToolTurn{
		UserMessage: executionFormsJSONToolMessage,
		ReadFileArguments: map[string]any{
			executionFormsToolPathKey: executionFormsJSONFixtureFile,
		},
		EditFileArguments: map[string]any{
			executionFormsToolPathKey: executionFormsJSONFixtureFile,
			executionFormsToolEditsKey: []map[string]any{{
				executionFormsToolOldTextKey: "before",
				executionFormsToolNewTextKey: "after",
			}},
		},
		RunCommandArguments: map[string]any{
			executionFormsToolCommandKey: executionFormsJSONCommand,
			executionFormsToolPurposeKey: executionFormsJSONPurpose,
		},
		FinalAnswer: executionFormsJSONAnswer,
	}
}

func patchToolTurn() testinfra.ScriptedToolTurn {
	return testinfra.ScriptedToolTurn{
		UserMessage: executionFormsPatchToolMessage,
		ReadFileArguments: map[string]any{
			executionFormsToolPathKey: executionFormsPatchFixtureFile,
		},
		ApplyPatchArguments: map[string]any{
			executionFormsToolPatchKey: executionFormsPatchText,
		},
		RunCommandArguments: map[string]any{
			executionFormsToolCommandKey: executionFormsPatchCommand,
			executionFormsToolPurposeKey: executionFormsPatchPurpose,
		},
		FinalAnswer: executionFormsPatchAnswer,
	}
}
