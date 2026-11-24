package worker_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDockerProfileEnvironmentAcceptsNonSecretSettings(t *testing.T) {
	t.Parallel()

	space := dockerSpace(testProfile)
	space.Env = map[string]string{
		"BUILD_MODE": "release",
		"_cache2":    "",
		"lowerCase":  "space and = are valid values",
	}
	space.GitName = "Example Developer"
	space.GitEmail = "developer@example.invalid"

	require.NoError(t, space.Validate())
}

func TestDockerProfileEnvironmentRefusesReservedKeys(t *testing.T) {
	t.Parallel()

	for _, key := range []string{
		"PATH", "HOME", "USER", "LOGNAME", "DOCKER_HOST",
		"BASH_ENV", "ENV", "BASHOPTS", "SHELLOPTS", "PS4", "GCONV_PATH",
		"LD_PRELOAD", "LD_LIBRARY_PATH", "LD_FUTURE_SETTING",
		"PEEN_API_TOKEN", "PEEN_STATE_DIR", "PEEN_CONFIG_DIR",
		"PEEN_WORKER_UID", "PEEN_WORKER_ALLOW_SUDO", "PEEN_FUTURE_KEY",
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_AUTHOR_DATE",
		"GIT_AUTHOR_FUTURE", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
		"GIT_COMMITTER_DATE", "GIT_COMMITTER_FUTURE",
	} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			space := dockerSpace(testProfile)
			space.Env = map[string]string{key: "EXAMPLE-DO-NOT-USE"}

			err := space.Validate()
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
			assert.NotContains(t, err.Error(), "EXAMPLE-DO-NOT-USE")
		})
	}
}

func TestDockerProfileEnvironmentRefusesMalformedKeys(t *testing.T) {
	t.Parallel()

	const keyLimit = 256

	for _, key := range []string{
		"", "2START", "HAS-DASH", "HAS.DOT", "HAS=EQUALS",
		"HAS SPACE", "HAS\nLINE", "HAS\x00NUL", "é",
		strings.Repeat("a", keyLimit+1),
	} {
		t.Run(strconv.Quote(key), func(t *testing.T) {
			t.Parallel()

			space := dockerSpace(testProfile)
			space.Env = map[string]string{key: "value"}

			require.ErrorIs(t, space.Validate(), commerr.ErrValidationFailed)
		})
	}
}

func TestDockerProfileEnvironmentBounds(t *testing.T) {
	t.Parallel()

	const (
		keyLimit      = 256
		valueLimit    = 32768
		identityLimit = 256
		entryLimit    = 128
	)

	space := dockerSpace(testProfile)
	space.Env = map[string]string{
		strings.Repeat("a", keyLimit): strings.Repeat("v", valueLimit),
	}
	space.GitName = strings.Repeat("n", identityLimit)
	space.GitEmail = strings.Repeat("e", identityLimit)
	require.NoError(t, space.Validate())

	space.Env["TOO_LONG"] = strings.Repeat("v", valueLimit+1)
	require.ErrorIs(t, space.Validate(), commerr.ErrValidationFailed)

	space.Env = make(map[string]string, entryLimit+1)
	for index := range entryLimit {
		space.Env["SETTING_"+strconv.Itoa(index)] = "value"
	}

	require.NoError(t, space.Validate())

	space.Env["ONE_TOO_MANY"] = "value"
	require.ErrorIs(t, space.Validate(), commerr.ErrValidationFailed)
}

func TestDockerProfileEnvironmentRejectsUnsafeValues(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"bad\x00value", "bad\rvalue", "bad\nvalue"} {
		t.Run(strconv.Quote(value), func(t *testing.T) {
			t.Parallel()

			space := dockerSpace(testProfile)
			space.Env = map[string]string{"SETTING": value}

			err := space.Validate()
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
			assert.NotContains(t, err.Error(), value)
		})
	}
}

func TestDockerProfileGitIdentityRejectsUnsafeValues(t *testing.T) {
	t.Parallel()

	const identityLimit = 256

	for _, value := range []string{
		"bad\x00value", "bad\rvalue", "bad\nvalue",
		strings.Repeat("x", identityLimit+1),
	} {
		t.Run(strconv.Quote(value), func(t *testing.T) {
			t.Parallel()

			space := dockerSpace(testProfile)
			space.GitName = value

			require.ErrorIs(t, space.Validate(), commerr.ErrValidationFailed)

			space.GitName = ""
			space.GitEmail = value

			require.ErrorIs(t, space.Validate(), commerr.ErrValidationFailed)
		})
	}
}

func TestControlSpaceRefusesProfileEnvironmentAndIdentity(t *testing.T) {
	t.Parallel()

	for _, mutate := range []func(*worker.AgentSpace){
		func(s *worker.AgentSpace) { s.Env = map[string]string{"A": "B"} },
		func(s *worker.AgentSpace) { s.GitName = "Example Developer" },
		func(s *worker.AgentSpace) { s.GitEmail = "dev@example.invalid" },
	} {
		space := worker.AgentSpace{
			Name: worker.AgentSpaceControl,
			Kind: worker.KindControl,
		}
		mutate(&space)

		require.ErrorIs(t, space.Validate(), commerr.ErrValidationFailed)
	}
}
