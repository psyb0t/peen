package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testMCPSecret = "fake-mcp-server-value" //nolint:gosec // Fake test value.

func TestMCPEnvNames(t *testing.T) {
	t.Parallel()

	config := Config{MCPEnv: " GITHUB_TOKEN, ,DOCS_TOKEN,GITHUB_TOKEN "}

	assert.Equal(t, []string{"GITHUB_TOKEN", "DOCS_TOKEN"},
		config.MCPEnvNames())
	assert.Empty(t, Config{}.MCPEnvNames())
}

func TestValidateMCP(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		env     string
		timeout time.Duration
		wantErr string
	}{
		{name: "valid", env: "GITHUB_TOKEN,_X1"},
		{name: "zero timeout takes the default", env: "", timeout: 0},
		{
			name: "negative timeout", timeout: -time.Second,
			wantErr: "PEEN_MCP_START_TIMEOUT is negative",
		},
		{
			name: "invalid name", env: "1BAD",
			wantErr: `invalid variable "1BAD"`,
		},
		{
			name: "Peen setting", env: "PEEN_API_TOKEN",
			wantErr: `may not name the Peen setting "PEEN_API_TOKEN"`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			config := Config{MCPEnv: tc.env, MCPStartTimeout: tc.timeout}

			err := config.validateMCP()
			if tc.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, ErrInvalidConfig)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// An MCP server secret reaches the worker, which hands it to the server, and
// never the commands the agent runs.
func TestMCPSecretsReachWorkersAndNotCommands(t *testing.T) {
	t.Setenv("MCP_TEST_TOKEN", testMCPSecret)

	config := testConfig(t)
	config.MCP = true
	config.MCPEnv = "MCP_TEST_TOKEN,MCP_TEST_UNSET"

	environment, err := config.WorkerEnvironment()
	require.NoError(t, err)
	assert.Equal(t, testMCPSecret, environment["MCP_TEST_TOKEN"])
	assert.NotContains(t, environment, "MCP_TEST_UNSET",
		"an unset variable is left out")

	config.MCP = false

	environment, err = config.WorkerEnvironment()
	require.NoError(t, err)
	assert.NotContains(t, environment, "MCP_TEST_TOKEN",
		"with MCP off no secret leaves the controller")

	commands, err := config.CommandEnvironment([]string{
		"PATH=/usr/bin", "MCP_TEST_TOKEN=" + testMCPSecret,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"PATH=/usr/bin"}, commands)
}
