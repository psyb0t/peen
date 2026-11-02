//go:build integration

package api_test

import (
	"path"
	"strings"
	"testing"

	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	scopedRuleTestWorkspacePrefix = "scoped-rule-"
	scopedRuleTestFile            = ".agents/rules/go.md"
	scopedRuleTestBody            = "Go rule: run gofmt before you finish."
	scopedRuleTestDocument        = "---\npaths:\n  - \"**/*.go\"\n---\n" +
		scopedRuleTestBody + "\n"
	scopedRuleTestGoFile      = "cmd/app/main.go"
	scopedRuleTestMessage     = "read the Go file twice"
	scopedRuleTestFinalAnswer = "the Go file was read"
	scopedRuleTestOpening     = `<rule name="go" source="`

	blankAgentTestMessage     = "say hello as a blank agent"
	blankAgentTestEnvironment = "PEEN_BLANK_AGENT"
	blankAgentTestBasePrompt  = "You are a coding agent."
	blankAgentTestCoreRules   = "# Peen operating rules"
)

// A rule with paths frontmatter stays out of the system prompt and reaches
// the model from the session's worker after the first read of a matching
// file, once, without its frontmatter.
func TestAPIScopedRuleLoadsAfterTheFirstMatchingRead(t *testing.T) {
	workspace := openFreshWorkspace(t, scopedRuleTestWorkspacePrefix,
		map[string]string{
			scopedRuleTestFile:   scopedRuleTestDocument,
			scopedRuleTestGoFile: "package main\n",
		})
	connection := openSessionConnection(t, workspace.sessionID)
	goFile := path.Join(workspace.directory, scopedRuleTestGoFile)

	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: scopedRuleTestMessage,
		Calls: []testinfra.ScriptedToolCall{
			{
				Name:      "readFile",
				Arguments: map[string]any{"path": workspace.fixturePath},
			},
			{Name: "readFile", Arguments: map[string]any{"path": goFile}},
			{Name: "readFile", Arguments: map[string]any{"path": goFile}},
		},
		FinalAnswer: scopedRuleTestFinalAnswer,
	})

	before := len(integrationInfra.ProviderRequests())

	sendSessionMessage(t, connection, workspace.sessionID, scopedRuleTestMessage)
	readFramesUntilTurnCompleted(t, connection)

	requests := providerRequestsSince(before)
	require.Len(t, requests, 4)

	assert.NotContains(t, requests[0][0].Content, scopedRuleTestBody,
		"a scoped rule is not part of the system prompt")
	assert.Zero(t, apiRuleBlockCount(requests[1]),
		"a file no pattern matches loads nothing")

	afterMatch := nonSystemMessages(t, requests[2])
	last := afterMatch[len(afterMatch)-1]
	assert.Equal(t, "user", last.Role)
	assert.True(t, strings.HasPrefix(last.Content, scopedRuleTestOpening),
		last.Content)
	assert.Contains(t, last.Content, scopedRuleTestBody)
	assert.NotContains(t, last.Content, "paths:")

	assert.Equal(t, 1, apiRuleBlockCount(requests[3]),
		"a rule the conversation holds is not sent again")
}

// PEEN_BLANK_AGENT removes Peen's base prompt, operating rules, core skills,
// and core agent inside the worker, and a turn still runs.
func TestAPIBlankAgentRunsWithoutBuiltInInstructions(t *testing.T) {
	variant := startAPIVariant(t, testinfra.VariantOptions{
		Environment:      map[string]string{blankAgentTestEnvironment: "true"},
		UnsetEnvironment: []string{variantTestAgentSetter},
	})
	sessionID := openVariantSession(t, variant)
	connection := dialVariantSession(t, variant, sessionID)

	sendSessionMessage(t, connection, sessionID, blankAgentTestMessage)
	readFramesUntilTurnCompleted(t, connection)

	prompt := variant.LastSystemPrompt()
	assert.NotContains(t, prompt, blankAgentTestBasePrompt)
	assert.NotContains(t, prompt, blankAgentTestCoreRules)
	assert.NotContains(t, prompt, variantTestAgentBody)
	assert.NotContains(t, prompt, variantTestFreshnessEntry)
	assert.NotContains(t, prompt, variantTestPlanningEntry)
	assert.NotContains(t, prompt, variantTestGeneralPurposeEntry)
	assert.False(t, strings.HasPrefix(prompt, "\n"),
		"an empty base leaves no leading gap")
}

func apiRuleBlockCount(messages []testinfra.ProviderMessage) int {
	count := 0

	for _, message := range messages {
		if message.Role != "user" {
			continue
		}

		count += strings.Count(message.Content, scopedRuleTestOpening)
	}

	return count
}
