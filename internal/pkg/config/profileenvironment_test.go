package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDockerProfileParsesEnvironmentAndGitIdentity(t *testing.T) {
	t.Parallel()

	config := withSocket(testConfig(t), absentSocket(t))
	config.DockerProfilesJSON = `[{
		"name":"ops",
		"env":{"BUILD_MODE":"release"},
		"gitName":"Example Developer",
		"gitEmail":"developer@example.invalid"
	}]`

	spaces, err := config.AgentSpaces()
	require.NoError(t, err)

	space, err := spaces.Select(testOpsSpace)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"BUILD_MODE": "release"}, space.Env)
	assert.Equal(t, "Example Developer", space.GitName)
	assert.Equal(t, "developer@example.invalid", space.GitEmail)
}

func TestDockerProfileRefusesCredentialAndBootstrapEnvironment(t *testing.T) {
	t.Parallel()

	for _, key := range []string{
		"PROVIDER_TOKEN", "MCP_TOKEN", "BASH_ENV", "ENV", "BASHOPTS",
		"SHELLOPTS", "PS4", "GCONV_PATH", "LD_PRELOAD", "LD_LIBRARY_PATH",
	} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			config := withSocket(testConfig(t), absentSocket(t))
			config.UpstreamsJSON = `[{
				"name":"provider",
				"baseUrl":"http://localhost:1234",
				"apiKeyEnv":"PROVIDER_TOKEN"
			}]`
			config.MCPEnv = "MCP_TOKEN"

			profiles, err := json.Marshal([]DockerProfile{{
				Name: testOpsProfile,
				Env:  map[string]string{key: "EXAMPLE-DO-NOT-USE"},
			}})
			require.NoError(t, err)

			config.DockerProfilesJSON = string(profiles)

			_, err = config.AgentSpaces()
			require.ErrorIs(t, err, ErrInvalidConfig)
			assert.NotContains(t, err.Error(), "EXAMPLE-DO-NOT-USE")
		})
	}
}

func TestDockerProfileRefusesMalformedEnvironmentJSON(t *testing.T) {
	t.Parallel()

	for _, profile := range []string{
		`{"name":"ops","env":{"SETTING":3}}`,
		`{"name":"ops","env":["SETTING=value"]}`,
		`{"name":"ops","gitName":3}`,
		`{"name":"ops","gitEmail":3}`,
		`{"name":"ops","git_name":"Example"}`,
	} {
		t.Run(profile, func(t *testing.T) {
			t.Parallel()

			config := withSocket(testConfig(t), absentSocket(t))
			config.DockerProfilesJSON = "[" + profile + "]"

			_, err := config.AgentSpaces()
			require.ErrorIs(t, err, ErrInvalidConfig)
		})
	}
}
