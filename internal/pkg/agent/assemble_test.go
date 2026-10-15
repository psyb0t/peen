package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const assembleTestCoreAgent = "core:general-purpose"

func assembleFailureOptions(
	t *testing.T,
	rootAgent string,
	harnessOptions ...harness.ResolverOption,
) AssembleOptions {
	t.Helper()

	root := t.TempDir()
	configDirectory := filepath.Join(root, "config")
	require.NoError(t, os.MkdirAll(configDirectory, runtimeTestDirectoryMode))

	return AssembleOptions{
		StateDirectory: filepath.Join(root, "state"),
		Runtime: RuntimeOptions{
			RootAgent:       rootAgent,
			ConfigDirectory: configDirectory,
		},
		HarnessOptions: harnessOptions,
	}
}

func TestAssembleFailsWhenTheRootAgentResolvesToNothing(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		rootAgent string
		options   []harness.ResolverOption
		wantText  string
	}{
		{
			name:      "disabled core agent",
			rootAgent: assembleTestCoreAgent,
			options: []harness.ResolverOption{
				harness.WithDisabledCoreAgents([]string{"general-purpose"}),
			},
			wantText: "PEEN_DISABLED_CORE_AGENTS",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := Assemble(
				t.Context(),
				assembleFailureOptions(t, tc.rootAgent, tc.options...),
			)

			require.ErrorIs(t, err, harness.ErrAgentNotFound)
			assert.Contains(t, err.Error(), tc.wantText)
		})
	}
}

func TestAssembleOwnsSessionAndWorkflowStorage(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		inMemory bool
	}{
		{name: "durable"},
		{name: "in memory", inMemory: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var options RuntimeOptions
			newRuntimeFixtureWithOptions(t,
				elelemtest.NewScriptedDriver(),
				func(value *RuntimeOptions) { options = *value },
			)
			assembled, err := Assemble(t.Context(), AssembleOptions{
				Runtime:        options,
				StateDirectory: t.TempDir(),
				InMemoryStore:  tc.inMemory,
			})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, assembled.Handle.Close()) })
			require.NotNil(t, assembled.Handle.Workflows)
			_, err = assembled.Handle.Workflows.GetStats(t.Context())
			require.NoError(t, err)
			require.NoError(t, assembled.Handle.SQLDB().PingContext(t.Context()))
			require.NoError(t, assembled.Handle.Close())
			_, err = assembled.Handle.Workflows.GetStats(t.Context())
			require.Error(t, err)
			require.Error(t, assembled.Handle.SQLDB().PingContext(t.Context()))
			require.NoError(t, assembled.Handle.Close())
		})
	}
}

func TestHarnessOptionsDisableTheConfiguredCoreSets(t *testing.T) {
	t.Parallel()

	fixture := newSkillTestFixture(t)

	options, err := HarnessOptions(config.Config{
		DisabledCoreSkills: " planning ",
		DisabledCoreAgents: "all",
	})
	require.NoError(t, err)

	resolver, err := harness.NewResolver(
		fixture.configRoot,
		harness.Limits{},
		options...,
	)
	require.NoError(t, err)

	snapshot, err := resolver.Resolve(fixture.workspace)
	require.NoError(t, err)

	_, err = snapshot.ActivateSkill("core:planning")
	require.ErrorIs(t, err, harness.ErrSkillNotFound)

	_, err = snapshot.ActivateSkill("core:freshness")
	require.NoError(t, err)

	assert.Empty(t, snapshot.Agents())
}

func TestHarnessOptionsRejectUnknownNames(t *testing.T) {
	t.Parallel()

	_, err := HarnessOptions(config.Config{DisabledCoreSkills: "bogus"})
	require.ErrorIs(t, err, config.ErrInvalidConfig)

	_, err = HarnessOptions(config.Config{DisabledCoreAgents: "bogus"})
	require.ErrorIs(t, err, config.ErrInvalidConfig)
}
