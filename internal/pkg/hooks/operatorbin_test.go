package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/psyb0t/commander"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHooksFindOnlyTheOperatorBinCommands(t *testing.T) {
	t.Parallel()
	snapshot, workspace := testSnapshot(t, `version: 1
preToolUse:
  - actions:
      - type: command
        command: operator-check
`, "")
	operatorBin := filepath.Join(snapshot.ConfigRoot(), ".agents", "bin")
	workspaceBin := filepath.Join(workspace, ".agents", "bin")
	for _, directory := range []string{operatorBin, workspaceBin} {
		require.NoError(t, os.MkdirAll(directory, 0o700))
	}

	const executable = "#!/bin/sh\nprintf '%s' '{\"message\":\"operator command\"}'\n"
	require.NoError(t, os.WriteFile(filepath.Join(operatorBin,
		"operator-check"), []byte(executable), 0o600))
	//nolint:gosec // The fixture must be executable by the test owner.
	require.NoError(t, os.Chmod(filepath.Join(operatorBin,
		"operator-check"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(workspaceBin,
		"operator-check"), []byte("#!/bin/sh\nexit 77\n"), 0o600))
	//nolint:gosec // The fixture must be executable by the test owner.
	require.NoError(t, os.Chmod(filepath.Join(workspaceBin,
		"operator-check"), 0o700))
	for _, backend := range []struct {
		name string
		run  CommandRunner
	}{
		{name: "native"},
		{name: "commander", run: WithCommander(commander.New())},
	} {
		t.Run(backend.name, func(t *testing.T) {
			t.Parallel()
			runner, err := New(Options{
				Snapshot: snapshot, Workspace: workspace,
				RunCommand: backend.run,
			})
			require.NoError(t, err)
			outcome, err := runner.Run(t.Context(), Invocation{
				Event: harness.HookEventPreToolUse,
			})
			require.NoError(t, err)
			assert.Equal(t, []string{"operator command"}, outcome.Injections)
		})
	}
}

func TestHookEnvironmentPrependsTheOperatorBin(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := filepath.Join(root, ".agents", "bin")
	require.NoError(t, os.MkdirAll(bin, 0o700))
	runner := Runner{configDirectory: root}
	_, environment, err := runner.operatorCommand(harness.HookAction{
		Command:     "uninstalled-command",
		Environment: map[string]string{"PATH": "/usr/bin", "KEEP": "yes"},
	})
	require.NoError(t, err)
	assert.Equal(t, bin+":/usr/bin", environment["PATH"])
	assert.Equal(t, "yes", environment["KEEP"])
	_, absent, err := (Runner{}).operatorCommand(harness.HookAction{
		Command: "uninstalled-command",
	})
	require.NoError(t, err)
	assert.NotContains(t, absent, "PATH")
}
