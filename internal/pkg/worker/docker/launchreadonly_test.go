package docker_test

import (
	"io/fs"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/psyb0t/peen/internal/pkg/worker/docker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readonlySpace() worker.AgentSpace {
	return grantedSpace(func(s *worker.AgentSpace) {
		s.Docker = worker.DockerAccessReadonly
	})
}

// readonlyLauncher builds a launcher whose proxy starts are recorded instead
// of listening on a socket.
func readonlyLauncher(
	t *testing.T,
	daemon *fakeDaemon,
	started *[]proxyStart,
	startErr, closeErr error,
) *docker.Launcher {
	t.Helper()

	launcher, err := docker.New(docker.Options{
		Client:           daemon,
		Identity:         testIdentity(),
		DockerSocketPath: dockerSocketPath,
		StartReadonlyProxy: fakeProxyStarter(
			daemon,
			started,
			startErr,
			closeErr,
		),
	})
	require.NoError(t, err)

	return launcher
}

// The proxy listens before the container exists and forwards to the
// controller's own socket, and the worker's DOCKER_HOST names that proxy.
// Stopping the worker closes the proxy after the container is removed.
func TestLaunchStartsTheReadonlyProxyBeforeCreate(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	document := testDocument(sessionID)
	daemon := &fakeDaemon{
		labels:      worker.ContainerLabels(sessionID),
		repoDigests: []string{testImage},
	}
	started := []proxyStart{}

	launcher := readonlyLauncher(t, daemon, &started, nil, nil)

	process, err := launcher.Launch(t.Context(), worker.LaunchRequest{
		Document:   document,
		AgentSpace: readonlySpace(),
	})
	require.NoError(t, err)

	require.Len(t, started, 1)
	assert.Equal(t, docker.ReadonlyProxySocketPath(document), started[0].socketPath)
	assert.Equal(t, dockerSocketPath, started[0].upstream)
	assert.Equal(
		t,
		"unix://"+started[0].socketPath,
		daemon.created.Env["DOCKER_HOST"],
	)
	assert.Equal(t, []string{callProxyStart, callCreate}, daemon.calls)

	for _, mount := range daemon.created.Mounts {
		assert.NotEqual(t, dockerSocketPath, mount.Target)
		assert.NotEqual(t, dockerSocketPath, mount.Source)
	}

	assert.Empty(t, daemon.created.Groups)

	require.NoError(t, process.Stop(t.Context()))
	require.NoError(t, process.Stop(t.Context()), "a second Stop is harmless")

	assert.Equal(
		t,
		[]string{callProxyStart, callCreate, callRemove, callProxyClose},
		daemon.calls,
	)
}

// Every way a launch can fail after the proxy started closes the proxy, so a
// failed launch leaves no socket forwarding to the daemon.
func TestLaunchClosesTheReadonlyProxyOnEveryAbandonPath(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		configure func(*fakeDaemon)
		wantCalls []string
	}{
		{
			name:      "create fails",
			configure: func(d *fakeDaemon) { d.createErr = commerr.ErrConflict },
			wantCalls: []string{callProxyStart, callCreate, callProxyClose},
		},
		{
			name:      "attach fails",
			configure: func(d *fakeDaemon) { d.attachErr = commerr.ErrConflict },
			wantCalls: []string{
				callProxyStart,
				callCreate,
				callRemove,
				callProxyClose,
			},
		},
		{
			name:      "start fails",
			configure: func(d *fakeDaemon) { d.startErr = commerr.ErrConflict },
			wantCalls: []string{
				callProxyStart,
				callCreate,
				callRemove,
				callProxyClose,
			},
		},
		{
			// The inspect that follows start fails, and so does the one
			// cleanup needs, so the container cannot be removed. The proxy
			// still closes.
			name: "inspect fails",
			configure: func(d *fakeDaemon) {
				d.inspectErr = commerr.ErrConflict
			},
			wantCalls: []string{callProxyStart, callCreate, callProxyClose},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sessionID := uuid.New()
			daemon := &fakeDaemon{
				labels:      worker.ContainerLabels(sessionID),
				repoDigests: []string{testImage},
			}
			tc.configure(daemon)

			started := []proxyStart{}
			launcher := readonlyLauncher(t, daemon, &started, nil, nil)

			_, err := launcher.Launch(t.Context(), worker.LaunchRequest{
				Document:   testDocument(sessionID),
				AgentSpace: readonlySpace(),
			})

			require.ErrorIs(t, err, commerr.ErrConflict)
			assert.Len(t, started, 1)
			assert.Equal(t, tc.wantCalls, daemon.calls)
		})
	}
}

// A proxy that cannot start stops the launch before any container exists.
func TestLaunchFailsWhenTheReadonlyProxyCannotStart(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	daemon := &fakeDaemon{
		labels:      worker.ContainerLabels(sessionID),
		repoDigests: []string{testImage},
	}
	started := []proxyStart{}

	launcher := readonlyLauncher(
		t,
		daemon,
		&started,
		commerr.ErrPermissionDenied,
		nil,
	)

	_, err := launcher.Launch(t.Context(), worker.LaunchRequest{
		Document:   testDocument(sessionID),
		AgentSpace: readonlySpace(),
	})

	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	assert.Len(t, started, 1)
	assert.Empty(t, daemon.calls, "no container may be created")
}

// A readonly worker needs the controller's own socket to forward to. Without
// one the launch fails clearly instead of starting a worker that cannot reach
// the daemon it was promised.
func TestLaunchWithReadonlyRefusesWithoutAControllerSocket(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	daemon := &fakeDaemon{labels: worker.ContainerLabels(sessionID)}
	started := []proxyStart{}

	launcher, err := docker.New(docker.Options{
		Client:   daemon,
		Identity: testIdentity(),
		StartReadonlyProxy: fakeProxyStarter(
			daemon,
			&started,
			nil,
			nil,
		),
	})
	require.NoError(t, err)

	_, err = launcher.Launch(t.Context(), worker.LaunchRequest{
		Document:   testDocument(sessionID),
		AgentSpace: readonlySpace(),
	})

	require.ErrorIs(t, err, worker.ErrDockerAuthorityUnavailable)
	assert.Empty(t, started)
	assert.Empty(t, daemon.calls)
}

// Other agent spaces never start a proxy.
func TestLaunchStartsNoProxyWithoutReadonly(t *testing.T) {
	t.Parallel()

	for _, access := range []worker.DockerAccess{
		worker.DockerAccessNone,
		worker.DockerAccessFull,
	} {
		t.Run(string(access), func(t *testing.T) {
			t.Parallel()

			sessionID := uuid.New()
			daemon := &fakeDaemon{
				labels:      worker.ContainerLabels(sessionID),
				repoDigests: []string{testImage},
			}
			started := []proxyStart{}

			launcher, err := docker.New(docker.Options{
				Client:           daemon,
				Identity:         testIdentity(),
				DockerSocketPath: dockerSocketPath,
				DockerSocketGID:  testDockerGID,
				StartReadonlyProxy: fakeProxyStarter(
					daemon,
					&started,
					nil,
					nil,
				),
			})
			require.NoError(t, err)

			process, err := launcher.Launch(t.Context(), worker.LaunchRequest{
				Document: testDocument(sessionID),
				AgentSpace: grantedSpace(func(s *worker.AgentSpace) {
					s.Docker = access
				}),
			})
			require.NoError(t, err)
			require.NoError(t, process.Stop(t.Context()))

			assert.Empty(t, started)
			assert.NotContains(t, daemon.calls, callProxyClose)
		})
	}
}

// A container that is not this controller's to stop keeps no Docker access:
// the proxy closes even though the stop is refused.
func TestStopClosesTheReadonlyProxyWhenStopFails(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	daemon := &fakeDaemon{
		labels: map[string]string{
			worker.LabelManaged: worker.LabelManagedValue,
			worker.LabelSession: uuid.NewString(),
		},
		repoDigests: []string{testImage},
	}
	started := []proxyStart{}

	launcher := readonlyLauncher(t, daemon, &started, nil, nil)

	process, err := launcher.Launch(t.Context(), worker.LaunchRequest{
		Document:   testDocument(sessionID),
		AgentSpace: readonlySpace(),
	})
	require.NoError(t, err)

	require.ErrorIs(t, process.Stop(t.Context()), commerr.ErrPermissionDenied)
	assert.Empty(t, daemon.removed)
	assert.Equal(
		t,
		[]string{callProxyStart, callCreate, callProxyClose},
		daemon.calls,
	)
}

// A proxy that fails to close is reported by Stop, not hidden.
func TestStopReportsAReadonlyProxyCloseFailure(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	daemon := &fakeDaemon{
		labels:      worker.ContainerLabels(sessionID),
		repoDigests: []string{testImage},
	}
	started := []proxyStart{}

	launcher := readonlyLauncher(
		t,
		daemon,
		&started,
		nil,
		commerr.ErrLockHeld,
	)

	process, err := launcher.Launch(t.Context(), worker.LaunchRequest{
		Document:   testDocument(sessionID),
		AgentSpace: readonlySpace(),
	})
	require.NoError(t, err)

	require.ErrorIs(t, process.Stop(t.Context()), commerr.ErrLockHeld)
	assert.Equal(t, []string{testContainerID}, daemon.removed)
}

// Without a starter in the options the launcher runs the real proxy: a socket
// appears in the session directory at launch and is gone after Stop.
func TestLaunchRunsTheRealReadonlyProxyByDefault(t *testing.T) {
	t.Parallel()

	socketRoot, err := os.MkdirTemp("", "peen-dl-")
	require.NoError(t, err)

	t.Cleanup(func() { assert.NoError(t, os.RemoveAll(socketRoot)) })

	sessionID := uuid.New()
	document := testDocument(sessionID)
	document.SocketPath = worker.SocketPathFor(socketRoot, sessionID)

	require.NoError(t, os.MkdirAll(
		worker.SessionSocketDirectory(socketRoot, sessionID),
		0o700,
	))

	daemon := &fakeDaemon{
		labels:      worker.ContainerLabels(sessionID),
		repoDigests: []string{testImage},
	}

	launcher, err := docker.New(docker.Options{
		Client:           daemon,
		Identity:         testIdentity(),
		DockerSocketPath: dockerSocketPath,
	})
	require.NoError(t, err)

	process, err := launcher.Launch(t.Context(), worker.LaunchRequest{
		Document:   document,
		AgentSpace: readonlySpace(),
	})
	require.NoError(t, err)

	proxySocket := docker.ReadonlyProxySocketPath(document)

	info, err := os.Stat(proxySocket)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSocket)

	require.NoError(t, process.Stop(t.Context()))

	_, err = os.Stat(proxySocket)
	require.ErrorIs(t, err, fs.ErrNotExist)
}
