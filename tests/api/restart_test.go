//go:build integration

package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProductionImageRestartsWithDurableStateAndFreshHarness(t *testing.T) {
	require.NoError(t, integrationInfra.WriteWorkspaceFile(
		t.Context(),
		apiTestWorkspaceRulesFile,
		[]byte(apiTestInitialRules),
	))

	observation := sendAPIWebSocketMessage(t, apiTestRestartMessage)
	sessionID := observation.sessionID

	restartContext, cancelRestart := context.WithTimeout(
		t.Context(),
		apiTestRestartTimeout,
	)
	t.Cleanup(cancelRestart)
	require.NoError(t, integrationInfra.Restart(restartContext))

	messages := collectAllMessages(t, sessionID)
	assert.Contains(t, apiMessageContents(messages), apiTestRestartMessage)

	require.NoError(t, integrationInfra.WriteWorkspaceFile(
		t.Context(),
		apiTestWorkspaceRulesFile,
		[]byte(apiTestUpdatedRules),
	))
	_ = sendAPIWebSocketMessage(t, apiTestReloadMessage)
	assert.Contains(t, integrationInfra.LastSystemPrompt(), apiTestUpdatedRules)
}

// Descendant instructions load only when the agent enters their directory.
func TestProductionImageOmitsUnvisitedNestedInstructions(t *testing.T) {
	require.NoError(t, integrationInfra.WriteWorkspaceFile(
		t.Context(),
		apiTestNestedInstructionsFile,
		[]byte(apiTestNestedInstructions),
	))

	_ = sendAPIWebSocketMessage(t, apiTestNestedMessage)

	prompt := integrationInfra.LastSystemPrompt()
	assert.NotContains(t, prompt, apiTestNestedScopeLine)
	assert.NotContains(t, prompt, apiTestNestedInstructions)
	assert.Equal(
		t,
		1,
		strings.Count(prompt, testinfra.ConfigInstructions),
		"the config directory inside the workspace is read once",
	)
}
