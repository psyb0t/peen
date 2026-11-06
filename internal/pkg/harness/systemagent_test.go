package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testSystemConfigDirectory = "/srv/peen/config"
	testSystemStateDirectory  = "/srv/peen/state"
	testPlantedInstruction    = "planted instruction from a session"
)

func writeHarnessFile(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func systemResolver(t *testing.T, configRoot string) Resolver {
	t.Helper()

	resolver, err := NewResolver(
		configRoot,
		Limits{},
		WithSystemAgent(SystemAgentPaths{
			ConfigDirectory: testSystemConfigDirectory,
			StateDirectory:  testSystemStateDirectory,
		}),
	)
	require.NoError(t, err)

	return resolver
}

func TestSystemAgentNamesTheRealDirectories(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()

	snapshot, err := systemResolver(t, workspace).Resolve(workspace)
	require.NoError(t, err)

	agent, err := snapshot.Agent(SystemAgentName)
	require.NoError(t, err)

	assert.Contains(t, agent.Instructions, testSystemConfigDirectory)
	assert.Contains(t, agent.Instructions, testSystemStateDirectory)
	assert.NotContains(t, agent.Instructions, "{{")
}

func TestSystemAgentCannotLaunchChildren(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()

	snapshot, err := systemResolver(t, workspace).Resolve(workspace)
	require.NoError(t, err)

	agent, err := snapshot.Agent(SystemAgentName)
	require.NoError(t, err)

	assert.NotEmpty(t, agent.AllowedTools)
	assert.NotContains(t, agent.AllowedTools, "launchAgent")
	assert.Contains(t, agent.AllowedTools, "runCommand")
}

// A Docker session may be able to write the workspace's ancestors and the
// configuration layer, so nothing in them may reach the system agent.
func TestSystemAgentLoadsNoLayer(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()

	writeHarnessFile(
		t, filepath.Join(workspace, "AGENTS.md"), testPlantedInstruction,
	)
	writeHarnessFile(
		t,
		filepath.Join(workspace, ".agents", "hooks.yaml"),
		"hooks:\n  turnStart:\n    - command: echo planted\n",
	)
	writeHarnessFile(
		t,
		filepath.Join(workspace, ".agents", "agents", "planted.md"),
		"---\nname: planted\ndescription: Planted.\n---\nPlanted.\n",
	)

	resolver := systemResolver(t, workspace)

	// The runtime resolves through ResolveDirectory, and a turn that changes
	// directory resolves again, so every entry point has to hold.
	testCases := []struct {
		name    string
		resolve func(string) (Snapshot, error)
	}{
		{name: "resolve", resolve: resolver.Resolve},
		{name: "resolve directory", resolve: resolver.ResolveDirectory},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			snapshot, err := tc.resolve(workspace)
			require.NoError(t, err)

			assert.Empty(t, snapshot.Instructions())
			assert.Empty(t, snapshot.Skills())
			assert.Empty(t, snapshot.Hooks())
			assert.Empty(t, snapshot.EventHandlers())

			agents := snapshot.Agents()
			require.Len(t, agents, 1)
			assert.Equal(t, SystemAgentName, agents[0].Name)
		})
	}
}

func TestOrdinarySnapshotsNeverHoldTheSystemAgent(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()

	resolver, err := NewResolver(workspace, Limits{})
	require.NoError(t, err)

	snapshot, err := resolver.Resolve(workspace)
	require.NoError(t, err)

	_, err = snapshot.Agent(SystemAgentName)
	require.ErrorIs(t, err, ErrAgentNotFound)

	_, err = snapshot.Agent(systemAgentBareName)
	require.ErrorIs(t, err, ErrAgentNotFound)

	assert.NotContains(t, CoreAgentNames(), systemAgentBareName)
}

func TestCustomAgentCannotTakeTheReservedName(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()

	writeHarnessFile(
		t,
		filepath.Join(workspace, ".agents", "agents", "system.md"),
		"---\nname: system\ndescription: Impostor.\n---\nImpostor.\n",
	)

	resolver, err := NewResolver(workspace, Limits{})
	require.NoError(t, err)

	snapshot, err := resolver.Resolve(workspace)
	require.NoError(t, err)

	_, err = snapshot.Agent(systemAgentBareName)
	require.ErrorIs(t, err, ErrAgentNotFound)
	assert.NotEmpty(t, snapshot.Warnings())
}

func TestIsReservedAgentName(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		want bool
	}{
		{name: "system", want: true},
		{name: "core:system", want: true},
		{name: "core:general-purpose", want: false},
		{name: "systems", want: false},
		{name: "", want: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, IsReservedAgentName(tc.name))
		})
	}
}
