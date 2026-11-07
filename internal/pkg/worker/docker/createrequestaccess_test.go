package docker_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/psyb0t/peen/internal/pkg/worker/docker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `docker: full` receives the host socket, plus the group needed to use it.
func TestBuildCreateRequestGrantsTheDeclaredDockerSocket(t *testing.T) {
	t.Parallel()

	space := grantedSpace(func(s *worker.AgentSpace) {
		s.Docker = worker.DockerAccessFull
	})

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document:         testDocument(uuid.New()),
		AgentSpace:       space,
		Identity:         testIdentity(),
		DockerSocketPath: dockerSocketPath,
		DockerSocketGID:  testDockerGID,
	})
	require.NoError(t, err)

	assert.Contains(t, create.Groups, "999")

	granted := false

	for _, mount := range create.Mounts {
		if mount.Target != dockerSocketPath {
			continue
		}

		granted = true

		assert.Equal(t, dockerSocketPath, mount.Source)
	}

	assert.True(t, granted, "the declared Docker socket must be mounted")
}

// `docker: full` mounts the socket the controller resolved, not a hardcoded
// host path, and presents it at the conventional path inside the worker.
func TestBuildCreateRequestMountsTheResolvedSocket(t *testing.T) {
	t.Parallel()

	const relocated = "/run/user/1000/docker.sock"

	space := grantedSpace(func(s *worker.AgentSpace) {
		s.Docker = worker.DockerAccessFull
	})

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document:         testDocument(uuid.New()),
		AgentSpace:       space,
		Identity:         testIdentity(),
		DockerSocketPath: relocated,
		DockerSocketGID:  testDockerGID,
	})
	require.NoError(t, err)

	assert.Contains(
		t,
		create.Mounts,
		worker.Mount{Source: relocated, Target: dockerSocketPath},
	)

	for _, mount := range create.Mounts {
		assert.NotEqual(
			t,
			dockerSocketPath,
			mount.Source,
			"the host default path must not be mounted",
		)
	}
}

// A `docker: full` launch with no resolved socket is refused rather than
// mounting a guessed host path.
func TestBuildCreateRequestRefusesDockerFullWithoutASocketPath(t *testing.T) {
	t.Parallel()

	space := grantedSpace(func(s *worker.AgentSpace) {
		s.Docker = worker.DockerAccessFull
	})

	_, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document:        testDocument(uuid.New()),
		AgentSpace:      space,
		Identity:        testIdentity(),
		DockerSocketGID: testDockerGID,
	})

	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

// A socket owned by group 0 cannot be granted to the worker account, so
// startup refuses a `docker: full` profile instead of the first launch.
func TestValidateFullAccessSocketRefusesAnUngrantableGroup(t *testing.T) {
	t.Parallel()

	full := grantedSpace(func(s *worker.AgentSpace) {
		s.Name = worker.DockerSpaceName("full")
		s.Docker = worker.DockerAccessFull
	})

	withFull, err := worker.NewAgentSpaceSet(
		[]worker.AgentSpace{sandboxSpace(), full},
		sandboxSpace().Name,
	)
	require.NoError(t, err)

	sandboxOnly, err := worker.NewAgentSpaceSet(
		[]worker.AgentSpace{sandboxSpace()},
		sandboxSpace().Name,
	)
	require.NoError(t, err)

	require.ErrorIs(
		t,
		docker.ValidateFullAccessSocket(withFull, 0),
		commerr.ErrValidationFailed,
	)
	require.NoError(t, docker.ValidateFullAccessSocket(withFull, testDockerGID))
	require.NoError(t, docker.ValidateFullAccessSocket(sandboxOnly, 0))
}

// Every Docker worker gets a root-only identity bootstrap. Without the explicit
// escalation grant, the entrypoint writes no sudoers rule and the daemon sets
// no-new-privileges for the worker after the drop.
func TestBuildCreateRequestWithoutPrivilegeEscalation(t *testing.T) {
	t.Parallel()

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document:   testDocument(uuid.New()),
		AgentSpace: sandboxSpace(),
		Identity:   testIdentity(),
	})
	require.NoError(t, err)

	assert.Equal(t, "0:0", create.User)
	assert.NotContains(t, create.Env, "PEEN_WORKER_ALLOW_SUDO")
	assert.Equal(t, "1000", create.Env["PEEN_WORKER_UID"])
	assert.Equal(t, "peen", create.Env["PEEN_WORKER_USERNAME"])
	assert.True(t, create.NoNewPrivileges)
}

// A profile the operator marked `sudo: true` starts as root so the image
// entrypoint can build the host account and write its sudoers rule. The
// identity it hands that entrypoint is the controller's own, so the agent still
// ends up running as the host user rather than as root.
func TestBuildCreateRequestWithSudo(t *testing.T) {
	t.Parallel()

	space := grantedSpace(func(s *worker.AgentSpace) { s.Sudo = true })

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document:   testDocument(uuid.New()),
		AgentSpace: space,
		Identity:   testIdentity(),
	})
	require.NoError(t, err)

	assert.Equal(t, "0:0", create.User)
	assert.Equal(t, "true", create.Env["PEEN_WORKER_ALLOW_SUDO"])
	assert.Equal(t, "1000", create.Env["PEEN_WORKER_UID"])
	assert.Equal(t, "1000", create.Env["PEEN_WORKER_GID"])
	assert.Equal(t, "peen", create.Env["PEEN_WORKER_USERNAME"])
	assert.Equal(t, "/home/peen", create.Env["PEEN_WORKER_HOME"])
	assert.False(t, create.NoNewPrivileges)

	// The agent's own view of itself stays the host account either way.
	assert.Equal(t, "peen", create.Env["USER"])
	assert.Equal(t, "/home/peen", create.Env["HOME"])

	// Sudo alone grants no socket. That is a separate opt-in.
	for _, mount := range create.Mounts {
		assert.NotEqual(t, dockerSocketPath, mount.Target)
	}
}

// The two grants compose without either implying the other, and the socket
// group reaches the entrypoint so the account it creates can use the socket.
func TestBuildCreateRequestWithSudoAndDockerSocket(t *testing.T) {
	t.Parallel()

	space := grantedSpace(func(s *worker.AgentSpace) {
		s.Docker = worker.DockerAccessFull
		s.Sudo = true
	})

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document:         testDocument(uuid.New()),
		AgentSpace:       space,
		Identity:         testIdentity(),
		DockerSocketPath: dockerSocketPath,
		DockerSocketGID:  testDockerGID,
	})
	require.NoError(t, err)

	assert.Equal(t, "0:0", create.User)
	assert.Equal(t, "999", create.Env["PEEN_WORKER_SUPPLEMENTARY_GIDS"])
	assert.Contains(t, create.Groups, "999")
	assert.Equal(t, "true", create.Env["PEEN_WORKER_ALLOW_SUDO"])
}

// The network a profile names is the network the container request carries:
// `none` and `bridge` and `host` each reach the create call as that mode.
func TestBuildCreateRequestCarriesTheNetworkMode(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		mode worker.NetworkMode
	}{
		{name: "none", mode: worker.NetworkModeNone},
		{name: "bridge", mode: worker.NetworkModeBridge},
		{name: "host", mode: worker.NetworkModeHost},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			create, err := docker.BuildCreateRequest(docker.LaunchSpec{
				Document: testDocument(uuid.New()),
				AgentSpace: grantedSpace(func(s *worker.AgentSpace) {
					s.Network = tc.mode
				}),
				Identity: testIdentity(),
			})
			require.NoError(t, err)

			assert.Equal(t, tc.mode, create.NetworkMode)
		})
	}
}

// `docker: none` never mounts the socket, whatever else the profile grants.
func TestBuildCreateRequestWithDockerNoneMountsNoSocket(t *testing.T) {
	t.Parallel()

	space := grantedSpace(func(s *worker.AgentSpace) {
		s.Network = worker.NetworkModeHost
		s.Sudo = true
	})

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document:        testDocument(uuid.New()),
		AgentSpace:      space,
		Identity:        testIdentity(),
		DockerSocketGID: testDockerGID,
	})
	require.NoError(t, err)

	assert.Empty(t, create.Groups)

	for _, mount := range create.Mounts {
		assert.NotEqual(t, dockerSocketPath, mount.Target)
	}
}

// `docker: readonly` reaches the daemon only through the controller's proxy in
// the session's own socket directory. The worker gets no socket mount and no
// socket group, even when the controller knows both.
func TestBuildCreateRequestWithDockerReadonly(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	document := testDocument(sessionID)

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document: document,
		AgentSpace: grantedSpace(func(s *worker.AgentSpace) {
			s.Docker = worker.DockerAccessReadonly
		}),
		Identity:         testIdentity(),
		DockerSocketPath: dockerSocketPath,
		DockerSocketGID:  testDockerGID,
	})
	require.NoError(t, err)

	sessionDirectory := worker.SessionSocketDirectory(testSocketRoot, sessionID)
	proxySocket := filepath.Join(sessionDirectory, "docker.sock")

	assert.Equal(t, proxySocket, docker.ReadonlyProxySocketPath(document))
	assert.Equal(t, "unix://"+proxySocket, create.Env["DOCKER_HOST"])

	assert.Empty(t, create.Groups, "readonly needs no socket group")
	assert.NotContains(t, create.Env, "PEEN_WORKER_SUPPLEMENTARY_GIDS")

	targets := make([]string, 0, len(create.Mounts))
	for _, mount := range create.Mounts {
		targets = append(targets, mount.Target)

		assert.NotEqual(t, dockerSocketPath, mount.Source)
	}

	assert.ElementsMatch(
		t,
		[]string{testWorkspace, testConfigDir, sessionDirectory},
		targets,
		"the proxy socket arrives through the session directory mount",
	)
}

// Startup checks the socket root against the worker socket only. The proxy
// socket sits beside it, so a root at the longest accepted length must still
// fit the proxy socket in a Unix socket address.
func TestReadonlyProxySocketFitsEveryAcceptedSocketRoot(t *testing.T) {
	t.Parallel()

	const maxUnixSocketPathBytes = 107

	sessionID := uuid.New()
	base := worker.SocketPathFor("/r", sessionID)
	root := "/r" + strings.Repeat("x", maxUnixSocketPathBytes-len(base))

	require.NoError(t, worker.ValidateSocketRoot(root))
	require.Error(t, worker.ValidateSocketRoot(root+"x"))

	document := testDocument(sessionID)
	document.SocketPath = worker.SocketPathFor(root, sessionID)

	assert.LessOrEqual(
		t,
		len(docker.ReadonlyProxySocketPath(document)),
		maxUnixSocketPathBytes,
	)
}

// Only `docker: readonly` points the worker at a daemon through DOCKER_HOST.
// `full` uses the conventional socket path and `none` gets nothing.
func TestBuildCreateRequestSetsDockerHostOnlyForReadonly(t *testing.T) {
	t.Parallel()

	for _, access := range []worker.DockerAccess{
		worker.DockerAccessNone,
		worker.DockerAccessFull,
	} {
		t.Run(string(access), func(t *testing.T) {
			t.Parallel()

			create, err := docker.BuildCreateRequest(docker.LaunchSpec{
				Document: testDocument(uuid.New()),
				AgentSpace: grantedSpace(func(s *worker.AgentSpace) {
					s.Docker = access
				}),
				Identity:         testIdentity(),
				DockerSocketPath: dockerSocketPath,
				DockerSocketGID:  testDockerGID,
			})
			require.NoError(t, err)

			assert.NotContains(t, create.Env, "DOCKER_HOST")
		})
	}
}

// `docker: full` with no discovered socket group refuses rather than mounting
// a socket the worker account cannot use.
func TestBuildCreateRequestRefusesAnUnusableSocketGrant(t *testing.T) {
	t.Parallel()

	space := grantedSpace(func(s *worker.AgentSpace) {
		s.Docker = worker.DockerAccessFull
	})

	_, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document:         testDocument(uuid.New()),
		AgentSpace:       space,
		Identity:         testIdentity(),
		DockerSocketPath: dockerSocketPath,
	})

	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}
