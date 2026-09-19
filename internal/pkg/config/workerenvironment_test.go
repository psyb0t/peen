package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testWorkerProviderKey = "PEEN_TEST_WORKER_PROVIDER_KEY"

func TestWorkerEnvironmentCarriesRuntimeAndProviderConfiguration(t *testing.T) {
	t.Setenv("PEEN_DEFAULT_MODEL", testQualifiedModel)
	t.Setenv("PEEN_TOOL_MAX_EDITS", "17")
	t.Setenv(testWorkerProviderKey, "test-provider-credential")
	t.Setenv("PEEN_STATE_DIR", "/controller/state")
	t.Setenv("PEEN_API_TOKEN", "controller-api-token")
	t.Setenv("PEEN_DOCKER_SOCKET", "/var/run/docker.sock")
	t.Setenv("PEEN_WORKER_UID", "0")

	config := testConfig(t)
	config.UpstreamsJSON = `[{"name":"aigate","provider":"openai","apiKeyEnv":"` +
		testWorkerProviderKey + `"}]`

	environment, err := config.WorkerEnvironment()
	require.NoError(t, err)

	assert.Equal(t, testQualifiedModel, environment["PEEN_DEFAULT_MODEL"])
	assert.Equal(t, "17", environment["PEEN_TOOL_MAX_EDITS"])
	assert.Equal(
		t,
		"test-provider-credential",
		environment[testWorkerProviderKey],
	)
	assert.NotContains(t, environment, "PEEN_STATE_DIR")
	assert.NotContains(t, environment, "PEEN_API_TOKEN")
	assert.NotContains(t, environment, "PEEN_DOCKER_SOCKET")
	assert.NotContains(t, environment, "PEEN_WORKER_UID")
}

func TestWorkerEnvironmentRefusesAReservedProviderCredentialKey(t *testing.T) {
	t.Setenv("PEEN_STATE_DIR", "/controller/state")

	config := testConfig(t)
	config.UpstreamsJSON = `[{"name":"aigate","provider":"openai","apiKeyEnv":"PEEN_STATE_DIR"}]`

	_, err := config.WorkerEnvironment()

	require.ErrorIs(t, err, ErrInvalidConfig)
}

func TestConfigValidateDockerHostIdentity(t *testing.T) {
	base := testConfig(t)

	testCases := []struct {
		name     string
		username string
		home     string
		wantErr  bool
	}{
		{
			name: "both omitted for a native controller",
		},
		{
			name:     "complete Docker controller identity",
			username: "developer",
			home:     "/home/developer",
		},
		{
			name:     "username without home",
			username: "developer",
			wantErr:  true,
		},
		{
			name:    "home without username",
			home:    "/home/developer",
			wantErr: true,
		},
		{
			name:     "relative home",
			username: "developer",
			home:     "home/developer",
			wantErr:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := base
			config.HostUsername = tc.username
			config.HostHome = tc.home

			err := config.Validate()
			if tc.wantErr {
				require.ErrorIs(t, err, ErrInvalidConfig)

				return
			}

			require.NoError(t, err)
		})
	}
}
