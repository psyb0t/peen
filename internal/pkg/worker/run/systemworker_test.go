package run

import (
	"testing"

	peenconfig "github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testRealConfigDirectory = "/srv/peen/config"
	testSystemWorkspace     = "/srv/peen/state/system-agent"
)

func parsedDeployment() Options {
	return Options{
		ParseConfig: func() (peenconfig.Config, error) {
			return peenconfig.Config{
				Agent:      "reviewer",
				MCP:        true,
				AutoEvolve: true,
				BlankAgent: true,
			}, nil
		},
	}
}

// Every config-layer file loads from ConfigDirectory, so pointing it at the
// empty workspace is what keeps session-written files out of this worker.
func TestSystemWorkerIgnoresTheRealConfigDirectory(t *testing.T) {
	t.Parallel()

	deployment, err := workerConfig(parsedDeployment(), worker.LaunchDocument{
		Workspace:       testSystemWorkspace,
		ConfigDirectory: testRealConfigDirectory,
		System: &worker.SystemLaunch{
			ConfigDirectory: testRealConfigDirectory,
		},
	})
	require.NoError(t, err)

	assert.Equal(t, testSystemWorkspace, deployment.ConfigDirectory)
	assert.Equal(t, harness.SystemAgentName, deployment.RootAgent())
	assert.False(t, deployment.MCP)
	assert.False(t, deployment.AutoEvolve)
}

func TestOrdinaryWorkerKeepsItsConfiguration(t *testing.T) {
	t.Parallel()

	deployment, err := workerConfig(parsedDeployment(), worker.LaunchDocument{
		Workspace:       testSystemWorkspace,
		ConfigDirectory: testRealConfigDirectory,
	})
	require.NoError(t, err)

	assert.Equal(t, testRealConfigDirectory, deployment.ConfigDirectory)
	assert.Equal(t, "reviewer", deployment.RootAgent())
	assert.True(t, deployment.MCP)
	assert.True(t, deployment.AutoEvolve)
}
