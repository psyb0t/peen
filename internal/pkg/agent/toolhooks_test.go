package agent

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
