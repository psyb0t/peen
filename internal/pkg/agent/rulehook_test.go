package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	ruleHookGoFile    = "main.go"
	ruleHookGoContent = "package main\n"
	ruleHookFirstCall = "call_rule_first"
	ruleHookRetryCall = "call_rule_retry"
	ruleHookFinalText = "wrote it"

	ruleHookChildGoFile = "child.go"
	ruleHookChildFirst  = "call_child_rule_first"
	ruleHookChildRetry  = "call_child_rule_retry"

	// The rule carries " [" several times. That is also how an error's
	// source location begins, and a rule is markdown and code, so it has
	// to reach the model whole.
	ruleHookRuleBody = `RULEMARK go errors
- return errs []error, never a joined string
- see [the guide](docs/errors.md)
- [x] sentinels live in errors.go
`

	// This is deliver-rule.sh from docs/hooks.md, verbatim apart from line
	// continuations that keep this file within its line limit. It keeps a
	// marker per rule and per conversation in the hook's own stateDirectory.
	// The first matching write is denied with the rule as the reason, and
	// every later one passes.
	ruleHookScript = `#!/bin/sh
set -eu
invocation="$(cat)"
state="$(printf '%s' "$invocation" | jq -r '.stateDirectory')"
owner="$(printf '%s' "$invocation" | jq -r '.agentRunId // "root"')"
marker="$state/rule-$RULE_ID-$owner.delivered"
[ -f "$marker" ] && exit 0
: > "$marker"
exec jq -n --rawfile rule "$RULE_FILE" \
  '{decision: "deny", reason: $rule}'
`

	ruleHookHooksFormat = `version: 1
pre_write_file:
  - name: go-rules
    match:
      extensions: [".go"]
    actions:
      - name: deliver-go-rules
        type: command
        command: %s
        environment:
          RULE_FILE: %s
          RULE_ID: go
`

	ruleHookDenyReason = "return errs []error, see [the guide](docs/errors.md)"

	ruleHookDenyHooks = `version: 1
pre_write_file:
  - name: go-deny
    match:
      extensions: [".go"]
    actions:
      - name: deny-go
        type: deny
        reason: "` + ruleHookDenyReason + `"
`

	// This is forget-rules.sh from docs/hooks.md, verbatim. It removes the
	// delivery markers after a compaction may have dropped the rule.
	ruleHookForgetScript = `#!/bin/sh
set -eu
state="$(jq -r '.stateDirectory')"
find "$state" -maxdepth 1 -name 'rule-*.delivered' -delete
`

	ruleHookForgetHooksFormat = ruleHookHooksFormat + `post_compact:
  - name: forget-delivered-rules
    actions:
      - name: forget-rules
        type: command
        command: %s
`

	ruleHookCompactFirstFile  = "first.go"
	ruleHookCompactSecondFile = "second.go"
	ruleHookCompactReadCall   = "call_child_read_%d"
	ruleHookCompactFillReads  = 2
	ruleHookCompactBudget     = 900

	// ruleHookCompactReserve is the summary allowance. It makes a compaction
	// cover enough history that the second write and its retry both fit
	// before the budget is reached again.
	ruleHookCompactReserve = 250

	ruleHookScriptMode = 0o700
)

// A command hook can hand the model a project rule the first time it writes a
// matching file, then let the retry through. Nothing about this is built into
// Peen: it is one hooks.yaml group and one script.
//
// The model must receive the rule whole. A denial reason cut short would
// deliver half a rule while the marker records it as delivered.
func TestCommandHookDeliversRuleOnceThenAllowsTheRetry(t *testing.T) {
	writeInput, err := json.Marshal(tools.WriteFileInput{
		Path:    ruleHookGoFile,
		Content: ruleHookGoContent,
	})
	require.NoError(t, err)

	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			ruleHookFirstCall,
			toolNameWriteFile,
			string(writeInput),
		),
		elelemtest.ToolCall(
			ruleHookRetryCall,
			toolNameWriteFile,
			string(writeInput),
		),
		elelemtest.Text(ruleHookFinalText),
	)
	fixture := newRuntimeFixture(t, driver)
	installRuleHook(t, fixture)

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "write the go file",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	assert.Equal(t, ruleHookFinalText, result.Text)

	requests := driver.Requests()
	require.Len(t, requests, 3)

	rule := strings.TrimSpace(ruleHookRuleBody)

	assert.Contains(
		t,
		conversationText(requests[1:2]),
		rule,
		"the denied write must hand the model the whole rule",
	)

	written, err := os.ReadFile(
		filepath.Join(fixture.workspace, ruleHookGoFile),
	)
	require.NoError(t, err, "the retried write must go through")
	assert.Equal(t, ruleHookGoContent, string(written))

	assert.Equal(
		t,
		1,
		strings.Count(conversationText(requests[2:3]), rule),
		"a delivered rule must not be delivered again in the same session",
	)
}

// A child agent is a separate model context. A rule the root turn already
// received is not in the child's context, so the child must get it too, once.
// The script keys its marker on agentRunId to make that happen.
func TestCommandHookDeliversRuleSeparatelyToAChild(t *testing.T) {
	rootWrite := ruleHookWriteInput(t, ruleHookGoFile)
	childWrite := ruleHookWriteInput(t, ruleHookChildGoFile)

	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(ruleHookFirstCall, toolNameWriteFile, rootWrite),
		elelemtest.ToolCall(ruleHookRetryCall, toolNameWriteFile, rootWrite),
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  "write the child go file",
				Agent: launchAgentChildName,
			}),
		),
		elelemtest.ToolCall(ruleHookChildFirst, toolNameWriteFile, childWrite),
		elelemtest.ToolCall(ruleHookChildRetry, toolNameWriteFile, childWrite),
		elelemtest.Text("child wrote it"),
		elelemtest.Text(ruleHookFinalText),
	)
	fixture := childHarnessFixture(t, driver)
	installRuleHook(t, fixture)

	result := runChildLaunchTurn(t, fixture)
	assert.Equal(t, ruleHookFinalText, result.Text)

	rule := strings.TrimSpace(ruleHookRuleBody)
	childRequests, rootRequests := splitChildRequests(t, driver)

	assert.Equal(
		t,
		1,
		strings.Count(conversationText(childRequests[len(childRequests)-1:]), rule),
		"the child must receive the rule once, in its own context",
	)
	assert.Equal(
		t,
		1,
		strings.Count(conversationText(rootRequests[len(rootRequests)-1:]), rule),
		"the root must not receive the rule again for the child's write",
	)

	for _, name := range []string{ruleHookGoFile, ruleHookChildGoFile} {
		_, statErr := os.Stat(filepath.Join(fixture.workspace, name))
		assert.NoError(t, statErr, "the retried write of %s must land", name)
	}
}

func ruleHookWriteInput(t *testing.T, path string) string {
	t.Helper()

	encoded, err := json.Marshal(tools.WriteFileInput{
		Path:    path,
		Content: ruleHookGoContent,
	})
	require.NoError(t, err)

	return string(encoded)
}

// installRuleHook writes the rule and the delivery script into the test's own
// directory, outside the workspace the agent can reach, and installs the hook.
func installRuleHook(t *testing.T, fixture runtimeFixture) {
	t.Helper()

	scriptRoot := t.TempDir()
	rulePath := filepath.Join(scriptRoot, "go-rules.md")
	scriptPath := filepath.Join(scriptRoot, "deliver-go-rules.sh")

	require.NoError(t, os.WriteFile(
		rulePath,
		[]byte(ruleHookRuleBody),
		runtimeTestFileMode,
	))
	require.NoError(t, os.WriteFile(
		scriptPath,
		[]byte(ruleHookScript),
		ruleHookScriptMode,
	))
	writeChildHarnessHooks(
		t,
		fixture,
		fmt.Sprintf(ruleHookHooksFormat, scriptPath, rulePath),
	)
}

// A compaction can summarize the rule out of the model's context, so the
// post_compact forget script clears the markers and the next matching write
// gets the rule again. Without that, the model would keep writing Go with no
// rule in view.
//
// A child compacts inside one parent turn, which makes the whole cycle fit in
// a single scripted run: deliver, compact, forget, deliver again.
func TestPostCompactForgetScriptRedeliversTheRule(t *testing.T) {
	firstWrite := ruleHookWriteInput(t, ruleHookCompactFirstFile)
	secondWrite := ruleHookWriteInput(t, ruleHookCompactSecondFile)

	// With a flat charge per message, the child crosses the budget on its
	// fifth request: after the denied write, its retry, and two reads.
	turns := make([]elelemtest.Turn, 0, ruleHookCompactFillReads+7)
	turns = append(
		turns,
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  "write two go files",
				Agent: launchAgentChildName,
			}),
		),
		elelemtest.ToolCall(ruleHookChildFirst, toolNameWriteFile, firstWrite),
		elelemtest.ToolCall(ruleHookChildRetry, toolNameWriteFile, firstWrite),
	)

	for read := range ruleHookCompactFillReads {
		turns = append(turns, elelemtest.ToolCall(
			fmt.Sprintf(ruleHookCompactReadCall, read),
			toolNameReadFile,
			runtimeToolArguments(t, ruleHookCompactFirstFile),
		))
	}

	turns = append(
		turns,
		elelemtest.ToolCall(
			ruleHookFirstCall,
			toolNameWriteFile,
			secondWrite,
		),
		elelemtest.ToolCall(
			ruleHookRetryCall,
			toolNameWriteFile,
			secondWrite,
		),
		elelemtest.Text("child wrote both"),
		elelemtest.Text(ruleHookFinalText),
	)

	driver := elelemtest.NewScriptedDriver(turns...).
		WithTokenCounter(childCompactionCounter{})
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
		Customize: func(o *RuntimeOptions) {
			o.CompactionMode = config.CompactionModeSummarize
			o.MaxContextTokens = ruleHookCompactBudget
			o.CompactionOutputTokens = ruleHookCompactReserve
		},
	})

	summarizer := &childCompactionSummarizer{}
	fixture.runtime.compactionOptions.Summarize = summarizer.summarize

	scriptRoot := t.TempDir()
	rulePath := filepath.Join(scriptRoot, "go-rules.md")
	scriptPath := filepath.Join(scriptRoot, "deliver-rule.sh")
	forgetPath := filepath.Join(scriptRoot, "forget-rules.sh")

	require.NoError(t, os.WriteFile(
		rulePath,
		[]byte(ruleHookRuleBody),
		runtimeTestFileMode,
	))
	require.NoError(t, os.WriteFile(
		scriptPath,
		[]byte(ruleHookScript),
		ruleHookScriptMode,
	))
	require.NoError(t, os.WriteFile(
		forgetPath,
		[]byte(ruleHookForgetScript),
		ruleHookScriptMode,
	))
	writeChildHarnessHooks(t, fixture, fmt.Sprintf(
		ruleHookForgetHooksFormat,
		scriptPath,
		rulePath,
		forgetPath,
	))

	result := runChildLaunchTurn(t, fixture)
	assert.Equal(t, ruleHookFinalText, result.Text)
	require.Positive(t, summarizer.count, "the child never compacted")

	run := onlyChildRun(t, fixture, result.SessionID)
	page, err := fixture.store.ListAgentRunMessages(
		context.Background(),
		result.SessionID,
		run.ID,
		session.ListAgentRunMessagesOptions{Limit: session.MaximumPageLimit},
	)
	require.NoError(t, err)

	rule := strings.TrimSpace(ruleHookRuleBody)
	deliveredTo := []string{}

	for _, message := range page.Items {
		if !strings.Contains(message.Content, rule) {
			continue
		}

		deliveredTo = append(deliveredTo, message.ToolCallID)
	}

	assert.Equal(
		t,
		[]string{ruleHookChildFirst, ruleHookFirstCall},
		deliveredTo,
		"the rule is delivered once before the compaction and once after it",
	)

	for _, name := range []string{
		ruleHookCompactFirstFile,
		ruleHookCompactSecondFile,
	} {
		_, statErr := os.Stat(filepath.Join(fixture.workspace, name))
		assert.NoError(t, statErr, "the retried write of %s must land", name)
	}
}

// A static deny action reaches the model verbatim too, with no internal wrap
// text in front of it and nothing cut after the first " [".
func TestDenyActionReasonReachesTheModelVerbatim(t *testing.T) {
	writeInput, err := json.Marshal(tools.WriteFileInput{
		Path:    ruleHookGoFile,
		Content: ruleHookGoContent,
	})
	require.NoError(t, err)

	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			ruleHookFirstCall,
			toolNameWriteFile,
			string(writeInput),
		),
		elelemtest.Text(ruleHookFinalText),
	)
	fixture := newRuntimeFixture(t, driver)

	writeChildHarnessHooks(t, fixture, ruleHookDenyHooks)

	_, err = fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "write the go file",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	requests := driver.Requests()
	require.Len(t, requests, 2)

	last := requests[1].Messages[len(requests[1].Messages)-1]
	assert.Equal(t, ruleHookDenyReason, last.Text())

	_, statErr := os.Stat(filepath.Join(fixture.workspace, ruleHookGoFile))
	assert.True(t, os.IsNotExist(statErr), "a denied write must not land")
}
