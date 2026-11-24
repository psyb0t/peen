package docker_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/worker/docker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateRequestCarriesProfileEnvironmentAndIdentity(t *testing.T) {
	t.Parallel()

	space := sandboxSpace()
	space.Env = map[string]string{"BUILD_MODE": "release"}
	space.GitName = "Example Developer"
	space.GitEmail = "developer@example.invalid"

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document:   testDocument(uuid.New()),
		AgentSpace: space,
		Identity:   testIdentity(),
	})
	require.NoError(t, err)
	assert.Equal(t, "release", create.Env["BUILD_MODE"])
	assert.Equal(t, space.GitName, create.Env["GIT_AUTHOR_NAME"])
	assert.Equal(t, space.GitName, create.Env["GIT_COMMITTER_NAME"])
	assert.Equal(t, space.GitEmail, create.Env["GIT_AUTHOR_EMAIL"])
	assert.Equal(t, space.GitEmail, create.Env["GIT_COMMITTER_EMAIL"])
	assert.Equal(t, "0:0", create.User)
	assert.True(t, create.NoNewPrivileges)
	assert.Empty(t, create.Entrypoint)
}

func TestCreateRequestRuntimeCredentialsOverrideProfileValues(t *testing.T) {
	t.Parallel()

	space := sandboxSpace()
	space.Env = map[string]string{"PROVIDER_TOKEN": "profile-value"}

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document:   testDocument(uuid.New()),
		AgentSpace: space,
		Identity:   testIdentity(),
		RuntimeEnvironment: map[string]string{
			"PROVIDER_TOKEN": "EXAMPLE-DO-NOT-USE",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "EXAMPLE-DO-NOT-USE", create.Env["PROVIDER_TOKEN"])
	assert.Equal(t, "profile-value", space.Env["PROVIDER_TOKEN"])
}

func TestCreateRequestRefusesProfileBootstrapOverrides(t *testing.T) {
	t.Parallel()

	for _, key := range []string{
		"PEEN_CONFIG_DIR", "PEEN_WORKER_UID", "PEEN_WORKER_GID",
		"PEEN_WORKER_USERNAME", "PEEN_WORKER_HOME",
		"PEEN_WORKER_SUPPLEMENTARY_GIDS", "PEEN_WORKER_ALLOW_SUDO",
		"PEEN_API_TOKEN", "DOCKER_HOST", "HOME", "USER", "LOGNAME",
		"PATH", "GIT_AUTHOR_NAME", "GIT_COMMITTER_EMAIL",
		"BASH_ENV", "ENV", "BASHOPTS", "SHELLOPTS", "PS4", "GCONV_PATH",
		"LD_PRELOAD", "LD_LIBRARY_PATH", "LD_FUTURE_SETTING",
	} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			space := sandboxSpace()
			space.Env = map[string]string{key: "override"}

			_, err := docker.BuildCreateRequest(docker.LaunchSpec{
				Document:   testDocument(uuid.New()),
				AgentSpace: space,
				Identity:   testIdentity(),
			})
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}
