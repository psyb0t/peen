package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/hooks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testHookCommandEnvironment = "PEEN_TEST_HOOK_COMMAND"
	testHookCommandEnabled     = "enabled"
	testHookStateMarker        = "read-marker"
	testDynamicDenialReason    = "A recorded read blocks this mutation."
)

func TestHookCommandHelper(_ *testing.T) {
	if os.Getenv(testHookCommandEnvironment) != testHookCommandEnabled {
		return
	}

	invocation := hooks.Invocation{}
	if err := json.NewDecoder(os.Stdin).Decode(&invocation); err != nil {
		os.Exit(1)
	}

	if invocation.StateDirectory == "" || invocation.ContextTokens <= 0 {
		os.Exit(1)
	}

	marker := filepath.Join(invocation.StateDirectory, testHookStateMarker)
	//nolint:exhaustive // This helper accepts only the two configured events.
	switch invocation.Event {
	case harness.HookEventPostReadFile:
		if err := os.WriteFile(
			marker,
			[]byte(strconv.Itoa(invocation.ContextTokens)),
			runtimeTestFileMode,
		); err != nil {
			os.Exit(1)
		}
	case harness.HookEventPreApplyPatch:
		contents, err := os.ReadFile(marker)
		if err != nil || strings.TrimSpace(string(contents)) == "" {
			os.Exit(1)
		}

		if _, err := fmt.Fprint(
			os.Stdout,
			`{"decision":"deny","reason":"`+testDynamicDenialReason+`"}`,
		); err != nil {
			os.Exit(1)
		}
	default:
		os.Exit(1)
	}

	os.Exit(0)
}

func TestRuntimeInjectsConfiguredLifecycleAndFileHookContext(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			"read-call",
			toolNameReadFile,
			`{"path":"hook-target.txt"}`,
		),
		elelemtest.Text("finished"),
	)
	fixture := newRuntimeFixture(t, driver)
	writeRuntimeFile(
		t,
		filepath.Join(fixture.configDirectory, ".agents", "hooks.yaml"),
		`version: 1
pre_user_message:
  - actions:
      - type: inject
        message: Pre-user context.
session_start:
  - actions:
      - type: inject
        message: Session-start context.
post_user_message:
  - actions:
      - type: inject
        message: Post-user context.
turn_start:
  - actions:
      - type: inject
        message: Turn-start context.
pre_read_file:
  - match:
      path: hook-target.txt
    actions:
      - type: inject
        message: Read-hook context.
`,
	)
	writeRuntimeFile(
		t,
		filepath.Join(fixture.workspace, "hook-target.txt"),
		"hook target\n",
	)

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "read the hook target",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	assert.Equal(t, "finished", result.Text)

	requests := driver.Requests()
	require.Len(t, requests, 2)
	for _, message := range []string{
		"Pre-user context.",
		"Session-start context.",
		"Post-user context.",
		"Turn-start context.",
	} {
		assert.Contains(t, requests[0].Messages[0].Text(), message)
	}
	assert.Contains(
		t,
		messageTexts(requests[1].Messages),
		"Configured hook context:\nRead-hook context.",
	)
}

func TestRuntimePreFileHookDenialSkipsTheHandler(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			"read-call",
			toolNameReadFile,
			`{"path":"denied.txt"}`,
		),
		elelemtest.Text("recovered"),
	)
	fixture := newRuntimeFixture(t, driver)
	writeRuntimeFile(
		t,
		filepath.Join(fixture.configDirectory, ".agents", "hooks.yaml"),
		`version: 1
pre_read_file:
  - match:
      path: denied.txt
    actions:
      - type: deny
        reason: This file is protected.
`,
	)
	writeRuntimeFile(
		t,
		filepath.Join(fixture.workspace, "denied.txt"),
		"protected content\n",
	)

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "read the protected file",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	assert.Equal(t, "recovered", result.Text)

	requests := driver.Requests()
	require.Len(t, requests, 2)
	assert.Contains(
		t,
		messageTexts(requests[1].Messages),
		"run hook action: This file is protected.",
	)
	assert.NotContains(t, messageTexts(requests[1].Messages), "protected content")
}

func TestRuntimeHookCommandStateDynamicallyDeniesMutation(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			"read-call",
			toolNameReadFile,
			`{"path":"target.txt"}`,
		),
		elelemtest.ToolCall(
			"patch-call",
			toolNameApplyPatch,
			`{"patch":"*** Begin Patch\n*** Update File: target.txt\n@@\n-old\n+new\n*** End Patch"}`,
		),
		elelemtest.Text("recovered"),
	)
	fixture := newRuntimeFixture(t, driver)
	writeRuntimeFile(
		t,
		filepath.Join(fixture.configDirectory, ".agents", "hooks.yaml"),
		fmt.Sprintf(`version: 1
post_read_file:
  - name: remember-read
    actions:
      - name: persist-read-state
        type: command
        command: %q
        args: ["-test.run=TestHookCommandHelper"]
        environment:
          PEEN_TEST_HOOK_COMMAND: enabled
pre_apply_patch:
  - name: enforce-read-state
    actions:
      - name: reject-expired-read
        type: command
        command: %q
        args: ["-test.run=TestHookCommandHelper"]
        environment:
          PEEN_TEST_HOOK_COMMAND: enabled
`, os.Args[0], os.Args[0]),
	)
	target := filepath.Join(fixture.workspace, "target.txt")
	writeRuntimeFile(t, target, "old\n")

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "Read target, then change it.",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	assert.Equal(t, "recovered", result.Text)

	contents, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "old\n", string(contents))

	requests := driver.Requests()
	require.Len(t, requests, 3)
	assert.Contains(
		t,
		strings.Join(messageTexts(requests[2].Messages), "\n"),
		testDynamicDenialReason,
	)
}

func TestFileHookEventMappingsAndPathInputs(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		tool      string
		arguments string
		wantPaths []string
		wantPre   harness.HookEvent
		wantPost  harness.HookEvent
		wantFail  harness.HookEvent
	}{
		{
			name:      "directory listing",
			tool:      toolNameListFiles,
			arguments: `{"path":"source"}`,
			wantPaths: []string{"source"},
			wantPre:   harness.HookEventPreListFiles,
			wantPost:  harness.HookEventPostListFiles,
			wantFail:  harness.HookEventListFilesFailure,
		},
		{
			name:      "text search",
			tool:      toolNameSearchText,
			arguments: `{"path":"source"}`,
			wantPaths: []string{"source"},
			wantPre:   harness.HookEventPreSearchText,
			wantPost:  harness.HookEventPostSearchText,
			wantFail:  harness.HookEventSearchTextFailure,
		},
		{
			name:      "directory creation",
			tool:      toolNameMakeDirectory,
			arguments: `{"path":"generated"}`,
			wantPaths: []string{"generated"},
			wantPre:   harness.HookEventPreMakeDirectory,
			wantPost:  harness.HookEventPostMakeDirectory,
			wantFail:  harness.HookEventMakeDirectoryFailure,
		},
		{
			name:      "command directory",
			tool:      toolNameRunCommand,
			arguments: `{"directory":"subdir"}`,
			wantPaths: []string{"subdir"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.wantPaths, rawToolPaths(tc.tool, []byte(tc.arguments)))
			if tc.wantPre == "" {
				return
			}

			gotPre := fileHookEvent(tc.tool, hookEventPre)
			gotPost := fileHookEvent(tc.tool, hookEventSuccess)
			gotFailure := fileHookEvent(tc.tool, hookEventFailure)
			assert.Equal(t, []harness.HookEvent{tc.wantPre}, gotPre)
			assert.Equal(t, []harness.HookEvent{tc.wantPost}, gotPost)
			assert.Equal(t, []harness.HookEvent{tc.wantFail}, gotFailure)
		})
	}
}
