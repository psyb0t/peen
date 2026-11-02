package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	scopedRuleTestBody     = "Go rule: run gofmt before you finish."
	scopedRuleTestDocument = "---\npaths:\n  - \"**/*.go\"\n---\n" +
		scopedRuleTestBody + "\n"
	scopedRuleTestOpening = `<rule name="go" source="`
)

func TestScopedRuleLoadsOnceAfterTheFirstMatchingFileTool(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("read-text", toolNameReadFile,
			runtimeToolArguments(t, "notes.txt")),
		twoToolCalls(
			elelem.ToolCallDelta{
				ID: "read-main", Name: toolNameReadFile,
				Arguments: runtimeToolArguments(t, "main.go"),
			},
			elelem.ToolCallDelta{
				Index: 1, ID: "read-util", Name: toolNameReadFile,
				Arguments: runtimeToolArguments(t, "pkg/util.go"),
			},
		),
		elelemtest.ToolCall("read-again", toolNameReadFile,
			runtimeToolArguments(t, "main.go")),
		elelemtest.Text("done"),
	)
	fixture := newRuntimeFixture(t, driver)
	writeScopedRuleWorkspace(t, fixture.workspace)
	writeChildHarnessHooks(t, fixture, `version: 1
postReadFile:
  - match:
      extensions: [".go"]
    actions:
      - type: inject
        message: `+hookInjectTestNote+`
`)

	_, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "read the files",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	requests := driver.Requests()
	require.Len(t, requests, 4)

	assert.Zero(t, ruleBlockCount(requests[1].Messages),
		"a file no rule matches loads nothing")

	afterRound := requests[2].Messages
	assert.Equal(t, 1, ruleBlockCount(afterRound),
		"two matching calls in one round load the rule once")

	injected := lastInjectedMessages(afterRound)
	require.Len(t, injected, 2)
	assert.True(t, strings.HasPrefix(injected[0], "<hook>\n"))
	assert.Contains(t, injected[0], "\n\n"+scopedRuleTestOpening,
		"the rule follows the hook context in the first call's message")
	assert.Contains(t, injected[0], scopedRuleTestBody)
	assert.NotContains(t, injected[0], "paths:",
		"the frontmatter never reaches the model")
	assert.NotContains(t, injected[1], scopedRuleTestOpening)

	assert.Equal(t, 1, ruleBlockCount(requests[3].Messages),
		"a rule the conversation holds is not loaded again")
}

func TestScopedRuleLoadsAgainWhenTheConversationNoLongerHoldsIt(
	t *testing.T,
) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("first-read", toolNameReadFile,
			runtimeToolArguments(t, "main.go")),
		elelemtest.Text("first turn done"),
		elelemtest.ToolCall("second-read", toolNameReadFile,
			runtimeToolArguments(t, "main.go")),
		elelemtest.Text("second turn done"),
	)
	fixture := newRuntimeFixture(t, driver)
	writeScopedRuleWorkspace(t, fixture.workspace)

	for _, message := range []string{"first", "second"} {
		_, err := fixture.runtime.Run(context.Background(), TurnRequest{
			Message:   message,
			Workspace: fixture.workspace,
		})
		require.NoError(t, err)
	}

	requests := driver.Requests()
	require.Len(t, requests, 4)
	assert.Equal(t, 1, ruleBlockCount(requests[1].Messages))
	assert.Equal(t, 1, ruleBlockCount(requests[3].Messages),
		"the stored history still holds the rule in the next turn")
}

func TestRuleBlockPositionsTrustsOnlyPeenInjections(t *testing.T) {
	t.Parallel()

	block := scopedRuleTestOpening + `/rules/go.md" paths="**/*.go">` +
		"\nbody\n</rule>"
	messages := []elelem.Message{
		{Role: elelem.RoleUser, Content: elelem.Text(block)},
		{Role: elelem.RoleUser, Content: elelem.Text(
			"<hook>\n<event>postReadFile</event>\n\nnote\n</hook>\n\n" +
				`<rule name="py" source="/rules/py.md" paths="*.py">` +
				"\nbody\n</rule>",
		)},
		{Role: elelem.RoleUser, Content: elelem.Text(
			"<message from=\"agent\">\n" +
				`<rule name="sh" source="/rules/sh.md" paths="*.sh">` +
				"\n</message>",
		)},
		{Role: elelem.RoleTool, Content: elelem.Text(
			`<rule name="md" source="/rules/md.md" paths="*.md">`,
		)},
	}

	assert.Equal(t, map[string]int{
		"/rules/go.md": 0,
		"/rules/py.md": 1,
	}, ruleBlockPositions(messages))
}

// A rule block far enough back counts as gone, because under drop-oldest the
// request may already leave it out while the saved conversation keeps it.
func TestHeldRulesDropsABlockPastTheRefreshDistance(t *testing.T) {
	t.Parallel()

	block := scopedRuleTestOpening + `/rules/go.md" paths="**/*.go">` +
		"\nbody\n</rule>"
	messages := []elelem.Message{
		{Role: elelem.RoleUser, Content: elelem.Text(block)},
		{
			Role:    elelem.RoleAssistant,
			Content: elelem.Text(strings.Repeat("later work ", 400)),
		},
	}

	counter := elelem.DefaultTokenCounter()
	following, err := counter.Count(messages, nil)
	require.NoError(t, err)

	testCases := []struct {
		name          string
		refreshTokens int
		wantHeld      bool
	}{
		{name: "no distance limit", refreshTokens: 0, wantHeld: true},
		{name: "block within the distance", refreshTokens: following + 1, wantHeld: true},
		{name: "block past the distance", refreshTokens: following, wantHeld: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			runtime := &toolHookRuntime{
				tokenCounter:      counter,
				ruleRefreshTokens: tc.refreshTokens,
			}

			held, err := runtime.heldRules(messages)
			require.NoError(t, err)

			_, isHeld := held["/rules/go.md"]
			assert.Equal(t, tc.wantHeld, isHeld)
		})
	}
}

func TestRuleBlockSourceReadsTheOpeningLine(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		line   string
		want   string
		wantOK bool
	}{
		{
			name:   "opening line",
			line:   `<rule name="go" source="/a/&quot;b.md" paths="*.go">`,
			want:   "/a/&quot;b.md",
			wantOK: true,
		},
		{name: "closing tag", line: "</rule>"},
		{name: "other tag", line: `<hook source="x">`},
		{name: "no source", line: `<rule name="go">`},
		{name: "unterminated source", line: `<rule name="go" source="/a`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := ruleBlockSource(tc.line)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRoundToolCallsFindsTheCallsAssistantMessage(t *testing.T) {
	t.Parallel()

	first := []elelem.ToolCall{{ID: "a"}, {ID: "b"}}
	second := []elelem.ToolCall{{ID: "c"}}
	messages := []elelem.Message{
		{Role: elelem.RoleAssistant, ToolCalls: first},
		{Role: elelem.RoleTool, ToolCallID: "a"},
		{Role: elelem.RoleAssistant, ToolCalls: second},
	}

	assert.Equal(t, first, roundToolCalls(messages, "b"))
	assert.Equal(t, second, roundToolCalls(messages, "c"))
	assert.Nil(t, roundToolCalls(messages, "missing"))
}

func writeScopedRuleWorkspace(t *testing.T, workspace string) {
	t.Helper()

	writeRuntimeFile(t, filepath.Join(
		workspace, ".agents", "rules", "go.md",
	), scopedRuleTestDocument)
	writeRuntimeFile(t, filepath.Join(workspace, "main.go"), "package main\n")
	writeRuntimeFile(t, filepath.Join(workspace, "pkg", "util.go"),
		"package pkg\n")
	writeRuntimeFile(t, filepath.Join(workspace, "notes.txt"), "notes\n")
}

func twoToolCalls(first, second elelem.ToolCallDelta) elelemtest.Turn {
	return elelemtest.Turn{Deltas: []elelem.Delta{
		{ToolCall: &first},
		{ToolCall: &second, FinishReason: elelem.FinishReasonToolCalls},
	}}
}

func ruleBlockCount(messages []elelem.Message) int {
	count := 0

	for _, message := range messages {
		if message.Role != elelem.RoleUser {
			continue
		}

		count += strings.Count(message.Text(), scopedRuleTestOpening)
	}

	return count
}

// lastInjectedMessages returns the user messages that follow the last
// assistant message: the injections after that round's tool results.
func lastInjectedMessages(messages []elelem.Message) []string {
	injected := make([]string, 0)

	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role == elelem.RoleAssistant {
			break
		}

		if message.Role == elelem.RoleUser {
			injected = append([]string{message.Text()}, injected...)
		}
	}

	return injected
}
