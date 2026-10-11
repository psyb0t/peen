package config

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const unixHostPrefix = "unix://"

// withDockerEnv replaces what the daemon lookup reads from the process: the
// DOCKER_HOST value and the platform default socket.
func withDockerEnv(config Config, host, defaultSocket string) Config {
	return config.WithDockerEnvironment(host, defaultSocket)
}

// silentSocket is a socket file whose listener accepts and drops every
// connection, so it exists but never answers the Docker API.
func silentSocket(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), testSocketName)

	listenConfig := net.ListenConfig{}

	listener, err := listenConfig.Listen(context.Background(), "unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			_ = conn.Close()
		}
	}()

	return path
}

// staleSocket is a socket file left behind by a daemon that stopped: the file
// is there and nothing listens on it.
func staleSocket(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), testSocketName)

	listenConfig := net.ListenConfig{}

	listener, err := listenConfig.Listen(context.Background(), "unix", path)
	require.NoError(t, err)

	unixListener, isUnix := listener.(*net.UnixListener)
	require.True(t, isUnix)
	unixListener.SetUnlinkOnClose(false)
	require.NoError(t, listener.Close())

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSocket)

	return path
}

// answeringTCPDaemon is a TCP listener standing in for a remote daemon.
func answeringTCPDaemon(t *testing.T) string {
	t.Helper()

	listenConfig := net.ListenConfig{}

	listener, err := listenConfig.Listen(
		context.Background(),
		"tcp",
		"127.0.0.1:0",
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			_ = conn.Close()
		}
	}()

	return "tcp://" + listener.Addr().String()
}

// closedTCPHost is a TCP address nothing listens on.
func closedTCPHost(t *testing.T) string {
	t.Helper()

	listenConfig := net.ListenConfig{}

	listener, err := listenConfig.Listen(
		context.Background(),
		"tcp",
		"127.0.0.1:0",
	)
	require.NoError(t, err)

	address := listener.Addr().String()
	require.NoError(t, listener.Close())

	return "tcp://" + address
}

// The socket rule applies when ANY candidate daemon is reachable. Pointing
// PEEN_DOCKER_SOCKET at a missing path, or DOCKER_HOST at a remote daemon, must
// not hide a daemon the agent can still reach.
func TestDockerAuthorityCountsEveryCandidate(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		explicit      func(*testing.T) string
		host          func(*testing.T) string
		defaultSocket func(*testing.T) string
		want          bool
	}{
		{
			name:          "a missing explicit socket beside a live default",
			explicit:      absentSocket,
			host:          nothing,
			defaultSocket: reachableSocket,
			want:          true,
		},
		{
			name:          "a missing explicit socket beside a live unix host",
			explicit:      absentSocket,
			host:          unixHost(reachableSocket),
			defaultSocket: absentSocket,
			want:          true,
		},
		{
			name:          "a live explicit socket alone",
			explicit:      reachableSocket,
			host:          nothing,
			defaultSocket: absentSocket,
			want:          true,
		},
		{
			name:          "a live unix host alone",
			explicit:      nothing,
			host:          unixHost(reachableSocket),
			defaultSocket: absentSocket,
			want:          true,
		},
		{
			name:          "a live default alone",
			explicit:      nothing,
			host:          nothing,
			defaultSocket: reachableSocket,
			want:          true,
		},
		{
			name:          "a remote daemon that answers",
			explicit:      nothing,
			host:          answeringTCPDaemon,
			defaultSocket: absentSocket,
			want:          true,
		},
		{
			name:          "a remote daemon beside a missing explicit socket",
			explicit:      absentSocket,
			host:          answeringTCPDaemon,
			defaultSocket: absentSocket,
			want:          true,
		},
		{
			name:          "a remote daemon that does not answer",
			explicit:      nothing,
			host:          closedTCPHost,
			defaultSocket: absentSocket,
			want:          false,
		},
		{
			name:          "nothing reachable anywhere",
			explicit:      absentSocket,
			host:          unixHost(absentSocket),
			defaultSocket: absentSocket,
			want:          false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			config := withDockerEnv(
				testConfig(t),
				tc.host(t),
				tc.defaultSocket(t),
			)
			config.DockerSocket = tc.explicit(t)

			assert.Equal(t, tc.want, config.HasDockerAuthority())
		})
	}
}

func nothing(*testing.T) string { return "" }

// unixHost renders a DOCKER_HOST naming the socket the given maker creates.
func unixHost(maker func(*testing.T) string) func(*testing.T) string {
	return func(t *testing.T) string {
		t.Helper()

		return unixHostPrefix + maker(t)
	}
}

// The socket the launcher uses is the first candidate that answers, in the
// order explicit setting, unix DOCKER_HOST, platform default.
func TestDockerSocketPathIsTheFirstReachableCandidate(t *testing.T) {
	t.Parallel()

	hostSocket := reachableSocket(t)
	defaultSocket := reachableSocket(t)

	t.Run("an unreachable explicit socket is skipped", func(t *testing.T) {
		t.Parallel()

		config := withDockerEnv(
			testConfig(t),
			unixHostPrefix+hostSocket,
			defaultSocket,
		)
		config.DockerSocket = absentSocket(t)

		assert.Equal(t, hostSocket, config.DockerSocketPath())
	})

	t.Run("the explicit socket wins when it answers", func(t *testing.T) {
		t.Parallel()

		explicit := reachableSocket(t)

		config := withDockerEnv(
			testConfig(t),
			unixHostPrefix+hostSocket,
			defaultSocket,
		)
		config.DockerSocket = explicit

		assert.Equal(t, explicit, config.DockerSocketPath())
	})

	t.Run("the platform default is the last resort", func(t *testing.T) {
		t.Parallel()

		config := withDockerEnv(
			testConfig(t),
			unixHostPrefix+absentSocket(t),
			defaultSocket,
		)

		assert.Equal(t, defaultSocket, config.DockerSocketPath())
	})

	t.Run("no reachable socket yields no path", func(t *testing.T) {
		t.Parallel()

		config := withDockerEnv(testConfig(t), "", absentSocket(t))
		config.DockerSocket = absentSocket(t)

		assert.Empty(t, config.DockerSocketPath())
	})

	t.Run("a remote daemon is not a local socket", func(t *testing.T) {
		t.Parallel()

		config := withDockerEnv(
			testConfig(t),
			answeringTCPDaemon(t),
			absentSocket(t),
		)

		assert.Empty(t, config.DockerSocketPath())
		assert.True(t, config.HasDockerAuthority())
	})
}

// Reachable means Peen can talk to the Docker API, not that a file exists. A
// socket nothing answers on is not usable by the agent either, so it does not
// trigger the rule.
func TestDockerAuthorityNeedsAnAnsweringDaemon(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		path func(*testing.T) string
	}{
		{name: "a socket that drops connections", path: silentSocket},
		{name: "a stale socket file", path: staleSocket},
		{
			name: "a regular file",
			path: func(t *testing.T) string {
				t.Helper()

				path := filepath.Join(t.TempDir(), testSocketName)
				require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))

				return path
			},
		},
		{
			name: "a directory",
			path: func(t *testing.T) string {
				t.Helper()

				return t.TempDir()
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			config := withDockerEnv(testConfig(t), "", absentSocket(t))
			config.DockerSocket = tc.path(t)

			assert.False(t, config.HasDockerAuthority())

			spaces, err := config.AgentSpaces()
			require.NoError(t, err)
			assert.Equal(t, worker.AgentSpaceControl, spaces.Default())
			assert.Contains(t, spaces.Names(), worker.AgentSpaceControl)
		})
	}
}

// The fallthrough bug: a stale PEEN_DOCKER_SOCKET used to turn the rule off
// while the platform default socket stayed reachable. Control must stay refused
// and unlisted.
func TestAgentSpacesRefuseControlBesideAFallbackSocket(t *testing.T) {
	t.Parallel()

	config := withDockerEnv(testConfig(t), "", reachableSocket(t))
	config.DockerSocket = absentSocket(t)

	spaces, err := config.AgentSpaces()
	require.NoError(t, err)
	assert.Equal(t, testDefaultName, spaces.Default())
	assert.NotContains(t, spaces.Names(), worker.AgentSpaceControl)

	config.DefaultAgentSpace = worker.AgentSpaceControl

	require.ErrorIs(t, config.Validate(), ErrControlSpaceRefused)
}

// A remote daemon that answers is host root through the agent as much as a
// local socket is.
func TestAgentSpacesRefuseControlBesideARemoteDaemon(t *testing.T) {
	t.Parallel()

	config := withDockerEnv(
		testConfig(t),
		answeringTCPDaemon(t),
		absentSocket(t),
	)

	spaces, err := config.AgentSpaces()
	require.NoError(t, err)
	assert.Equal(t, testDefaultName, spaces.Default())
	assert.NotContains(t, spaces.Names(), worker.AgentSpaceControl)

	config.DefaultAgentSpace = worker.AgentSpaceControl

	_, err = config.AgentSpaces()
	require.ErrorIs(t, err, ErrControlSpaceRefused)
}
