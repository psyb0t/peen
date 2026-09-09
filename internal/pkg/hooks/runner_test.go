package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errHookCommandFailed = errors.New("hook command failed")

type testPublisher struct {
	notices []events.Notice
	err     error
}

func (p *testPublisher) Publish(notice events.Notice) (events.Notice, error) {
	if p.err != nil {
		return events.Notice{}, p.err
	}

	p.notices = append(p.notices, notice)

	return notice, nil
}

func TestRunnerAppliesConfigHooksThenEnabledWorkspaceHooks(t *testing.T) {
	t.Parallel()

	snapshot, workspace := testSnapshot(t, `version: 1
pre_tool_use:
  - match:
      tool: write_file
      root: internal
      path: "**/*.go"
      extensions: [".go"]
      input:
        /expectedSha256:
          exists: true
    actions:
      - type: inject
        message: config write rule
`, `version: 1
pre_tool_use:
  - match:
      tool: write_file
    actions:
      - type: inject
        message: workspace write rule
`)

	invocation := Invocation{
		Event: harness.HookEventPreToolUse,
		Tool:  "write_file",
		Paths: []string{filepath.Join(workspace, "internal", "check.go")},
		Input: json.RawMessage(`{"expectedSha256":"abc"}`),
	}

	configOnly, err := New(Options{Snapshot: snapshot, Workspace: workspace})
	require.NoError(t, err)
	configOutcome, err := configOnly.Run(context.Background(), invocation)
	require.NoError(t, err)
	assert.Equal(t, []string{"config write rule"}, configOutcome.Injections)

	withWorkspace, err := New(Options{
		Snapshot:             snapshot,
		Workspace:            workspace,
		EnableWorkspaceHooks: true,
	})
	require.NoError(t, err)
	workspaceOutcome, err := withWorkspace.Run(context.Background(), invocation)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"config write rule",
		"workspace write rule",
	}, workspaceOutcome.Injections)
}

func TestRunnerPassesFullEventToCommandAndHonorsItsDecision(t *testing.T) {
	t.Parallel()

	snapshot, workspace := testSnapshot(t, `version: 1
pre_tool_use:
  - match:
      tool: read_file
    actions:
      - type: command
        command: check-file
        args: ["--strict"]
        environment:
          CHECK_MODE: strict
`, "")
	publisher := &testPublisher{}
	var received CommandInput
	runner, err := New(Options{
		Snapshot:  snapshot,
		Workspace: workspace,
		Publisher: publisher,
		ContextTokenCounter: func(
			context.Context,
			Invocation,
		) (int, error) {
			return 73, nil
		},
		RunCommand: func(_ context.Context, input CommandInput) ([]byte, error) {
			received = input

			return []byte(`{
  "message":"command guidance",
  "events":[{
    "type":"hook.checked",
    "summary":"checked",
    "data":{"ok":true},
    "delivery":"queue"
  }]
}`), nil
		},
	})
	require.NoError(t, err)

	sessionID := uuid.New()
	outcome, err := runner.Run(context.Background(), Invocation{
		Event:     harness.HookEventPreToolUse,
		SessionID: sessionID,
		Tool:      "read_file",
		CallID:    "call_1",
		Paths:     []string{filepath.Join(workspace, "readme.md")},
		Input:     json.RawMessage(`{"path":"readme.md"}`),
	})
	require.NoError(t, err)

	assert.Equal(t, "check-file", received.Command)
	assert.Equal(t, []string{"--strict"}, received.Args)
	assert.Equal(t, "strict", received.Environment["CHECK_MODE"])
	assert.Equal(t, workspace, received.WorkingDir)
	assert.Equal(t, []string{"command guidance"}, outcome.Injections)

	invocation := Invocation{}
	require.NoError(t, json.Unmarshal(received.Stdin, &invocation))
	assert.Equal(t, harness.HookEventPreToolUse, invocation.Event)
	assert.Equal(t, sessionID, invocation.SessionID)
	assert.Equal(t, "read_file", invocation.Tool)
	assert.Equal(t, 73, invocation.ContextTokens)
	assert.Equal(
		t,
		filepath.Join(snapshot.ConfigRoot(), hookStateDirectoryName, sessionID.String()),
		invocation.StateDirectory,
	)
	assertPrivateDirectory(t, filepath.Dir(invocation.StateDirectory))
	assertPrivateDirectory(t, invocation.StateDirectory)

	require.Len(t, publisher.notices, 1)
	assert.Equal(t, "hook.checked", publisher.notices[0].Type)
	assert.Equal(t, sessionID, publisher.notices[0].SessionID)
	assert.Equal(t, hookFailureEventSource, publisher.notices[0].Source)
}

func TestRunnerUsesStablePrivateStatePerSession(t *testing.T) {
	t.Parallel()

	snapshot, workspace := testSnapshot(t, `version: 1
pre_tool_use:
  - actions:
      - type: command
        command: state-check
`, "")
	invocations := make([]Invocation, 0, 3)
	runner, err := New(Options{
		Snapshot:  snapshot,
		Workspace: workspace,
		RunCommand: func(_ context.Context, input CommandInput) ([]byte, error) {
			invocation := Invocation{}
			require.NoError(t, json.Unmarshal(input.Stdin, &invocation))
			invocations = append(invocations, invocation)

			return nil, nil
		},
	})
	require.NoError(t, err)

	sessionA := uuid.New()
	sessionB := uuid.New()
	for _, sessionID := range []uuid.UUID{sessionA, sessionA, sessionB} {
		_, err = runner.Run(context.Background(), Invocation{
			Event:     harness.HookEventPreToolUse,
			SessionID: sessionID,
		})
		require.NoError(t, err)
	}

	require.Len(t, invocations, 3)
	assert.Equal(t, invocations[0].StateDirectory, invocations[1].StateDirectory)
	assert.NotEqual(t, invocations[0].StateDirectory, invocations[2].StateDirectory)
	for _, invocation := range invocations {
		relative, relErr := filepath.Rel(snapshot.ConfigRoot(), invocation.StateDirectory)
		require.NoError(t, relErr)
		assert.NotEqual(t, "..", relative)
		assert.False(t, strings.HasPrefix(relative, ".."+string(filepath.Separator)))
		assertPrivateDirectory(t, invocation.StateDirectory)
	}
}

func TestRunnerRejectsPreActionWhenTokenEstimateFails(t *testing.T) {
	t.Parallel()

	snapshot, workspace := testSnapshot(t, `version: 1
pre_tool_use:
  - actions:
      - type: command
        command: token-check
`, "")
	commandCalled := false
	runner, err := New(Options{
		Snapshot:  snapshot,
		Workspace: workspace,
		ContextTokenCounter: func(
			context.Context,
			Invocation,
		) (int, error) {
			return 0, errHookCommandFailed
		},
		RunCommand: func(context.Context, CommandInput) ([]byte, error) {
			commandCalled = true

			return nil, nil
		},
	})
	require.NoError(t, err)

	_, err = runner.Run(context.Background(), Invocation{
		Event:     harness.HookEventPreToolUse,
		SessionID: uuid.New(),
	})
	require.ErrorIs(t, err, errHookCommandFailed)
	assert.False(t, commandCalled)
}

func TestRunnerDebugLogsEveryHookLifecycleAndName(t *testing.T) {
	var captured bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&captured, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	t.Cleanup(func() { slog.SetDefault(originalLogger) })

	snapshot, workspace := testSnapshot(t, `version: 1
pre_tool_use:
  - name: protected-read
    match:
      tool: read_file
    actions:
      - name: record-check
        type: command
        command: check-file
`, "")
	publisher := &testPublisher{}
	runner, err := New(Options{
		Snapshot:  snapshot,
		Workspace: workspace,
		Publisher: publisher,
		RunCommand: func(context.Context, CommandInput) ([]byte, error) {
			return []byte(`{"events":[{"type":"hook.checked","delivery":"queue"}]}`), nil
		},
	})
	require.NoError(t, err)

	_, err = runner.Run(context.Background(), Invocation{
		Event:     harness.HookEventPreToolUse,
		SessionID: uuid.New(),
		Tool:      "read_file",
	})
	require.NoError(t, err)
	_, err = runner.Run(context.Background(), Invocation{
		Event: harness.HookEventPreReadFile,
		Tool:  "read_file",
	})
	require.NoError(t, err)

	logs := captured.String()
	for _, message := range []string{
		"hook lifecycle event started",
		"hook lifecycle event finished",
		"hook group started",
		"hook group finished",
		"hook action started",
		"hook action finished",
		"hook command started",
		"hook command finished",
		"hook session event started",
		"hook session event finished",
	} {
		assert.Contains(t, logs, message)
	}
	assert.Contains(t, logs, `"hook_name":"protected-read"`)
	assert.Contains(t, logs, `"hook_action_name":"record-check"`)
	assert.Contains(t, logs, `"hook_event":"pre_read_file"`)
}

func TestRunnerDeniesPreActionFailuresAndContinuesPostFailures(t *testing.T) {
	t.Parallel()

	snapshot, workspace := testSnapshot(t, `version: 1
pre_tool_use:
  - actions:
      - type: command
        command: pre-check
post_tool_use:
  - actions:
      - type: command
        command: post-check
`, "")
	publisher := &testPublisher{}
	runner, err := New(Options{
		Snapshot:  snapshot,
		Workspace: workspace,
		Publisher: publisher,
		RunCommand: func(_ context.Context, input CommandInput) ([]byte, error) {
			return nil, ctxerrors.Wrap(errHookCommandFailed, input.Command)
		},
	})
	require.NoError(t, err)

	preResult, err := runner.Run(context.Background(), Invocation{
		Event:     harness.HookEventPreToolUse,
		SessionID: uuid.New(),
	})
	assert.Empty(t, preResult.Injections)
	require.Error(t, err)
	assert.Contains(t, err.Error(), errHookCommandFailed.Error())

	sessionID := uuid.New()
	postResult, err := runner.Run(context.Background(), Invocation{
		Event:     harness.HookEventPostToolUse,
		SessionID: sessionID,
	})
	require.NoError(t, err)
	assert.Empty(t, postResult.Injections)
	require.Len(t, publisher.notices, 1)
	assert.Equal(t, hookFailureEventType, publisher.notices[0].Type)
	assert.Equal(t, sessionID, publisher.notices[0].SessionID)
}

func TestRunnerDenialNeverDegradesToPostFailure(t *testing.T) {
	t.Parallel()

	snapshot, workspace := testSnapshot(t, `version: 1
post_tool_use:
  - actions:
      - type: deny
        reason: Stop here.
`, "")
	runner, err := New(Options{Snapshot: snapshot, Workspace: workspace})
	require.NoError(t, err)

	_, err = runner.Run(context.Background(), Invocation{
		Event: harness.HookEventPostToolUse,
	})
	require.ErrorIs(t, err, ErrDenied)
}

func TestRunCommandCapsOutputAndDoesNotUseShell(t *testing.T) {
	t.Parallel()

	output, err := runCommand(context.Background(), CommandInput{
		Command:    "/usr/bin/printf",
		Args:       []string{"123456"},
		WorkingDir: t.TempDir(),
		MaxOutput:  4,
	})
	assert.Empty(t, output)
	require.ErrorIs(t, err, ErrCommandOutputLimit)
}

func TestMatchGlobAndInputMatching(t *testing.T) {
	t.Parallel()

	assert.True(t, matchGlob("**/*.go", "internal/test.go"))
	assert.True(t, matchGlob("**/*.go", "test.go"))
	assert.False(t, matchGlob("*.go", "internal/test.go"))
	assert.True(t, matchesInput(map[string]harness.HookInputMatch{
		"/nested/enabled": {
			Regex: "^true$",
		},
		"/name": {
			Equals: "fixture",
		},
	}, json.RawMessage(`{"nested":{"enabled":true},"name":"fixture"}`)))
}

func testSnapshot(
	t *testing.T,
	configHooks string,
	workspaceHooks string,
) (harness.Snapshot, string) {
	t.Helper()

	root := t.TempDir()
	configRoot := filepath.Join(root, "config")
	workspace := filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(configRoot, 0o700))
	require.NoError(t, os.MkdirAll(workspace, 0o700))

	if configHooks != "" {
		writeHookFixture(t, configRoot, configHooks)
	}
	if workspaceHooks != "" {
		writeHookFixture(t, workspace, workspaceHooks)
	}

	resolver, err := harness.NewResolver(configRoot, harness.Limits{})
	require.NoError(t, err)
	snapshot, err := resolver.Resolve(workspace)
	require.NoError(t, err)

	return snapshot, workspace
}

func writeHookFixture(t *testing.T, root string, content string) {
	t.Helper()

	directory := filepath.Join(root, ".agents")
	require.NoError(t, os.MkdirAll(directory, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(directory, "hooks.yaml"),
		[]byte(strings.TrimSpace(content)+"\n"),
		0o600,
	))
}

func assertPrivateDirectory(t *testing.T, path string) {
	t.Helper()

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}
