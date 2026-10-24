package docker_test

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/psyb0t/peen/internal/pkg/worker/docker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testWorkspace  = "/srv/work/project"
	testConfigDir  = "/etc/peen"
	testSocketRoot = "/run/peen/workers"
	testImage      = "psyb0t/peen@sha256:" +
		"1111111111111111111111111111111111111111111111111111111111111111"
	// testCredential is fixed test data, never a real secret. These tests
	// assert it is absent from everything the daemon receives.
	testCredential   = "the-one-time-credential" //nolint:gosec // Test fixture.
	testUID          = 1000
	testGID          = 1000
	testDockerGID    = 999
	testContainerID  = "container-abc"
	dockerSocketPath = "/var/run/docker.sock"
	testProfileName  = "sandbox"

	// testLocalImageID is the shape a container inspect reports. It is a local
	// content ID, not a repository digest, and the launcher must never record
	// it as one.
	testLocalImageID = "sha256:" +
		"2222222222222222222222222222222222222222222222222222222222222222"
)

func testIdentity() docker.HostIdentity {
	return docker.HostIdentity{
		Username:  "peen",
		GroupName: "peen",
		UID:       testUID,
		GID:       testGID,
		Home:      "/home/peen",
	}
}

func testDocument(sessionID uuid.UUID) worker.LaunchDocument {
	return worker.LaunchDocument{
		SessionID:       sessionID,
		GenerationID:    uuid.New(),
		Credential:      worker.Credential(testCredential),
		SocketPath:      worker.SocketPathFor(testSocketRoot, sessionID),
		Workspace:       testWorkspace,
		ConfigDirectory: testConfigDir,
		AgentSpace:      worker.DockerSpaceName(testProfileName),
	}
}

// sandboxSpace is a Docker agent space with every grant at its safe value.
func sandboxSpace() worker.AgentSpace {
	return worker.AgentSpace{
		Name:     worker.DockerSpaceName(testProfileName),
		Kind:     worker.KindDocker,
		Revision: 1,
		Image:    testImage,
		Docker:   worker.DockerAccessNone,
		Network:  worker.NetworkModeBridge,
	}
}

// grantedSpace is the sandbox with the given grants changed.
func grantedSpace(change func(*worker.AgentSpace)) worker.AgentSpace {
	space := sandboxSpace()
	change(&space)

	return space
}

// A worker container is named for its session and carries exactly the two
// ownership labels, so cleanup can recognise it and nothing else.
func TestBuildCreateRequestUsesDeterministicNameAndLabels(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document:   testDocument(sessionID),
		AgentSpace: sandboxSpace(),
		Identity:   testIdentity(),
	})
	require.NoError(t, err)

	assert.Equal(t, "peen-worker-"+sessionID.String(), create.Name)
	assert.Equal(
		t,
		map[string]string{
			"peen.managed": "true",
			"peen.session": sessionID.String(),
		},
		create.Labels,
	)
}

// The container runs the same worker subcommand a control worker runs, from the
// operator-selected image.
func TestBuildCreateRequestRunsTheWorkerCommand(t *testing.T) {
	t.Parallel()

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document:   testDocument(uuid.New()),
		AgentSpace: sandboxSpace(),
		Identity:   testIdentity(),
	})
	require.NoError(t, err)

	assert.Equal(t, []string{worker.WorkerCommand}, create.Command)
	assert.Equal(t, testImage, create.Image)
}

// The credential never reaches the container's command, environment, or
// labels. It crosses once over stdin, after creation.
func TestBuildCreateRequestNeverCarriesTheCredential(t *testing.T) {
	t.Parallel()

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document:   testDocument(uuid.New()),
		AgentSpace: sandboxSpace(),
		Identity:   testIdentity(),
	})
	require.NoError(t, err)

	assert.NotContains(t, strings.Join(create.Command, " "), testCredential)

	for key, value := range create.Env {
		assert.NotContains(
			t,
			value,
			testCredential,
			"environment entry %q must not carry the credential",
			key,
		)
	}

	for key, value := range create.Labels {
		assert.NotContains(
			t,
			value,
			testCredential,
			"label %q must not carry the credential",
			key,
		)
	}
}

// A Docker worker needs its model and provider settings just like a control
// worker, but controller state and the public control token must never cross
// the process boundary.
func TestBuildCreateRequestCarriesOnlyWorkerRuntimeEnvironment(t *testing.T) {
	t.Parallel()

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document:   testDocument(uuid.New()),
		AgentSpace: sandboxSpace(),
		Identity:   testIdentity(),
		RuntimeEnvironment: map[string]string{
			"PEEN_DEFAULT_MODEL": "aigate/test-model",
			"AIGATE_TOKEN":       "test-provider-credential",
		},
	})
	require.NoError(t, err)

	assert.Equal(t, "aigate/test-model", create.Env["PEEN_DEFAULT_MODEL"])
	assert.Equal(t, "test-provider-credential", create.Env["AIGATE_TOKEN"])
	assert.NotContains(t, create.Env, "PEEN_STATE_DIR")
	assert.NotContains(t, create.Env, "PEEN_API_TOKEN")
}

func TestBuildCreateRequestRefusesControllerEnvironment(t *testing.T) {
	t.Parallel()

	for _, key := range []string{
		"PEEN_CONFIG_DIR",
		"PEEN_STATE_DIR",
		"PEEN_API_TOKEN",
		"PEEN_WORKER_UID",
		"PEEN_DOCKER_SOCKET",
		"PEEN_WORKER_IMAGE",
		"PEEN_DOCKER_PROFILES",
		"PEEN_DEFAULT_AGENT_SPACE",
		"PEEN_WORKSPACE_ROOT",
		"PEEN_HTTP_LISTEN_ADDRESS",
		"PEEN_METRICS_LISTEN_ADDRESS",
		"PEEN_HOST_USERNAME",
		"PEEN_HOST_HOME",
		"PEEN_LOG_DIRECTORY",
		"PEEN_LOG_RETENTION",
		"DOCKER_HOST",
		"HOME",
		"USER",
		"LOGNAME",
	} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			_, err := docker.BuildCreateRequest(docker.LaunchSpec{
				Document:   testDocument(uuid.New()),
				AgentSpace: sandboxSpace(),
				Identity:   testIdentity(),
				RuntimeEnvironment: map[string]string{
					key: "must-not-cross",
				},
			})

			require.ErrorIs(t, err, commerr.ErrPermissionDenied)
		})
	}
}

func TestBuildCreateRequestCarriesTheHarnessSelectionEnvironment(
	t *testing.T,
) {
	t.Parallel()

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document:   testDocument(uuid.New()),
		AgentSpace: sandboxSpace(),
		Identity:   testIdentity(),
		RuntimeEnvironment: map[string]string{
			"PEEN_AGENT":                "reviewer",
			"PEEN_DISABLED_CORE_SKILLS": "planning",
			"PEEN_DISABLED_CORE_AGENTS": "all",
		},
	})
	require.NoError(t, err)

	assert.Equal(t, "reviewer", create.Env["PEEN_AGENT"])
	assert.Equal(t, "planning", create.Env["PEEN_DISABLED_CORE_SKILLS"])
	assert.Equal(t, "all", create.Env["PEEN_DISABLED_CORE_AGENTS"])
}

// The workspace, configuration, and session socket directory keep their literal
// host paths, so a path in a prompt, a hook, or a log means the same thing on
// both sides.
func TestBuildCreateRequestKeepsLiteralPaths(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document:   testDocument(sessionID),
		AgentSpace: sandboxSpace(),
		Identity:   testIdentity(),
	})
	require.NoError(t, err)

	assert.Equal(t, testWorkspace, create.WorkingDirectory)

	byTarget := map[string]worker.Mount{}
	for _, mount := range create.Mounts {
		byTarget[mount.Target] = mount
	}

	workspaceMount, found := byTarget[testWorkspace]
	require.True(t, found, "the workspace must be mounted")
	assert.Equal(t, testWorkspace, workspaceMount.Source)
	assert.False(t, workspaceMount.ReadOnly, "the workspace stays writable")

	configMount, found := byTarget[testConfigDir]
	require.True(t, found, "the configuration must be mounted")
	assert.Equal(t, testConfigDir, configMount.Source)
	assert.True(t, configMount.ReadOnly, "the configuration is read-only")

	sessionSocketDir := worker.SessionSocketDirectory(testSocketRoot, sessionID)

	socketMount, found := byTarget[sessionSocketDir]
	require.True(t, found, "the session socket directory must be mounted")
	assert.Equal(t, sessionSocketDir, socketMount.Source)
}

// A worker receives its own session's socket directory, never the shared root.
// The root holds every live session's socket, so mounting it would show one
// worker where every other session's controller surface lives.
func TestBuildCreateRequestMountsOnlyThisSessionsSocketDirectory(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	otherSessionID := uuid.New()

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document:   testDocument(sessionID),
		AgentSpace: sandboxSpace(),
		Identity:   testIdentity(),
	})
	require.NoError(t, err)

	otherSocket := worker.SocketPathFor(testSocketRoot, otherSessionID)

	for _, mount := range create.Mounts {
		assert.NotEqual(
			t,
			testSocketRoot,
			mount.Target,
			"the shared socket root must not be mounted",
		)
		assert.NotEqual(t, testSocketRoot, mount.Source)

		require.False(
			t,
			strings.HasPrefix(otherSocket, mount.Source+"/"),
			"mount %q exposes another session's socket", mount.Source,
		)
	}
}

// The daemon starts every worker as root only long enough for the entrypoint to
// create the control account, then the entrypoint drops to that account before
// the agent process begins.
func TestBuildCreateRequestRunsAsTheHostIdentity(t *testing.T) {
	t.Parallel()

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document:   testDocument(uuid.New()),
		AgentSpace: sandboxSpace(),
		Identity:   testIdentity(),
	})
	require.NoError(t, err)

	assert.Equal(t, "0:0", create.User)
	assert.Equal(t, "peen", create.Env["USER"])
	assert.Equal(t, "peen", create.Env["LOGNAME"])
	assert.Equal(t, "/home/peen", create.Env["HOME"])
	assert.Equal(t, testConfigDir, create.Env["PEEN_CONFIG_DIR"])
	assert.Equal(t, "1000", create.Env["PEEN_WORKER_UID"])
	assert.Equal(t, "1000", create.Env["PEEN_WORKER_GID"])
	assert.Equal(t, "peen", create.Env["PEEN_WORKER_USERNAME"])
	assert.True(t, create.NoNewPrivileges)
}

// A root or incomplete control identity refuses the launch rather than falling
// back to a fixed UID or an image-local user.
func TestBuildCreateRequestRefusesAnUnusableIdentity(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		identity docker.HostIdentity
		wantErr  error
	}{
		{
			name: "root",
			identity: docker.HostIdentity{
				Username: "root",
				Home:     "/root",
			},
			wantErr: commerr.ErrPermissionDenied,
		},
		{
			name: "no username",
			identity: docker.HostIdentity{
				UID:  testUID,
				GID:  testGID,
				Home: "/home/peen",
			},
			wantErr: commerr.ErrRequiredFieldNotSet,
		},
		{
			name: "relative home",
			identity: docker.HostIdentity{
				Username: "peen",
				UID:      testUID,
				GID:      testGID,
				Home:     "peen",
			},
			wantErr: commerr.ErrValidationFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := docker.BuildCreateRequest(docker.LaunchSpec{
				Document:   testDocument(uuid.New()),
				AgentSpace: sandboxSpace(),
				Identity:   tc.identity,
			})

			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

// A profile with every grant at its default gets no Docker socket, the bridge
// network, and no sudo. The Docker socket is a separate, opt-in grant only
// `docker: full` carries.
func TestBuildCreateRequestKeepsTheSandboxSealed(t *testing.T) {
	t.Parallel()

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document:   testDocument(uuid.New()),
		AgentSpace: sandboxSpace(),
		Identity:   testIdentity(),
	})
	require.NoError(t, err)

	assert.Equal(t, worker.NetworkModeBridge, create.NetworkMode)
	assert.Empty(t, create.Groups)

	for _, mount := range create.Mounts {
		assert.NotEqual(
			t,
			dockerSocketPath,
			mount.Target,
			"a sandbox worker must not receive the Docker socket",
		)
	}
}

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

// The worker image is PEEN_WORKER_IMAGE when set, else the profile's own image,
// else the image published with this build.
func TestBuildCreateRequestImagePrecedence(t *testing.T) {
	t.Parallel()

	const (
		override = "operator/override:1"
		fallback = "psyb0t/peen:build"
		profile  = "operator/profile:2"
	)

	testCases := []struct {
		name         string
		override     string
		profileImage string
		fallback     string
		want         string
	}{
		{
			name:         "the override wins over everything",
			override:     override,
			profileImage: profile,
			fallback:     fallback,
			want:         override,
		},
		{
			name:         "the profile image wins over the build image",
			profileImage: profile,
			fallback:     fallback,
			want:         profile,
		},
		{
			name:     "the build image is the last resort",
			fallback: fallback,
			want:     fallback,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			space := sandboxSpace()
			space.Image = tc.profileImage

			create, err := docker.BuildCreateRequest(docker.LaunchSpec{
				Document:      testDocument(uuid.New()),
				AgentSpace:    space,
				Identity:      testIdentity(),
				ImageOverride: tc.override,
				ImageFallback: tc.fallback,
			})
			require.NoError(t, err)

			assert.Equal(t, tc.want, create.Image)
		})
	}
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

// The control agent space is never launched as a container.
func TestBuildCreateRequestRefusesTheControlSpace(t *testing.T) {
	t.Parallel()

	_, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document: testDocument(uuid.New()),
		AgentSpace: worker.AgentSpace{
			Name: worker.AgentSpaceControl,
			Kind: worker.KindControl,
		},
		Identity: testIdentity(),
	})

	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

// fakeDaemon records what the launcher asked the daemon to do. It never speaks
// to a real daemon, so these tests need no socket and start no container.
type fakeDaemon struct {
	created docker.CreateRequest
	stdin   []byte
	started []string
	stopped []string
	removed []string
	labels  map[string]string
	running bool

	// repoDigests is what the daemon reports for the worker image. Nil means an
	// image the daemon knows only locally, which records no repository digest.
	repoDigests []string

	// loggedContainers records which containers the launcher followed, so a
	// test can prove a Docker worker's records reach the controller's sinks.
	loggedContainers []string

	// logPayload is the already demultiplexed output the follow returns.
	logPayload string

	// logErr fails the follow, which must degrade the audit trail without
	// failing the worker.
	logErr error

	// pulled records the references the launcher fetched, so a test can prove a
	// missing image is pulled rather than failing the launch.
	pulled []string

	// absentUntilPulled makes the first inspect report the image is missing,
	// which is what a host that has never seen it answers.
	absentUntilPulled bool

	// pullErr fails the pull.
	pullErr error

	// createErr, attachErr, startErr, and inspectErr fail those calls, which
	// drives each launch abandon path.
	createErr  error
	attachErr  error
	startErr   error
	inspectErr error

	// calls is the order of lifecycle calls, shared with fakeProxy, so a test
	// can prove the proxy lives exactly as long as the container.
	calls []string

	// staleName is a container that already holds the worker name before the
	// launch, as one a crashed controller left behind does, with its labels
	// and running state.
	staleName    string
	staleLabels  map[string]string
	staleRunning bool
}

const (
	callProxyStart = "proxyStart"
	callProxyClose = "proxyClose"
	callCreate     = "create"
	callRemove     = "remove"
)

// fakeProxy stands in for the readonly proxy and records its lifetime in the
// fake daemon's call order.
type fakeProxy struct {
	daemon   *fakeDaemon
	closeErr error
}

func (p *fakeProxy) Close() error {
	p.daemon.calls = append(p.daemon.calls, callProxyClose)

	return p.closeErr
}

type proxyStart struct {
	socketPath string
	upstream   string
}

// fakeProxyStarter records each start and hands back a fakeProxy, or fails
// with startErr.
func fakeProxyStarter(
	daemon *fakeDaemon,
	started *[]proxyStart,
	startErr, closeErr error,
) docker.ReadonlyProxyStarter {
	return func(
		_ context.Context,
		socketPath, upstreamSocketPath string,
	) (docker.ReadonlyProxy, error) {
		*started = append(*started, proxyStart{
			socketPath: socketPath,
			upstream:   upstreamSocketPath,
		})

		if startErr != nil {
			return nil, startErr
		}

		daemon.calls = append(daemon.calls, callProxyStart)

		return &fakeProxy{daemon: daemon, closeErr: closeErr}, nil
	}
}

func (d *fakeDaemon) PullImage(_ context.Context, reference string) error {
	d.pulled = append(d.pulled, reference)

	if d.pullErr != nil {
		return d.pullErr
	}

	d.absentUntilPulled = false

	return nil
}

func (d *fakeDaemon) InspectImage(
	_ context.Context,
	_ string,
) (docker.ImageIdentity, error) {
	if d.absentUntilPulled {
		return docker.ImageIdentity{}, commerr.ErrNotFound
	}

	return docker.ImageIdentity{
		ID:          testLocalImageID,
		RepoDigests: d.repoDigests,
	}, nil
}

func (d *fakeDaemon) StreamLogs(
	_ context.Context,
	containerID string,
) (io.ReadCloser, error) {
	d.loggedContainers = append(d.loggedContainers, containerID)

	if d.logErr != nil {
		return nil, d.logErr
	}

	return io.NopCloser(strings.NewReader(d.logPayload)), nil
}

func (d *fakeDaemon) CreateContainer(
	_ context.Context,
	request docker.CreateRequest,
) (string, error) {
	d.calls = append(d.calls, callCreate)

	if d.createErr != nil {
		return "", d.createErr
	}

	d.created = request

	return testContainerID, nil
}

func (d *fakeDaemon) AttachStdin(
	_ context.Context,
	_ string,
	document []byte,
) error {
	if d.attachErr != nil {
		return d.attachErr
	}

	d.stdin = document

	return nil
}

func (d *fakeDaemon) StartContainer(_ context.Context, id string) error {
	if d.startErr != nil {
		return d.startErr
	}

	d.started = append(d.started, id)
	d.running = true

	return nil
}

func (d *fakeDaemon) StopContainer(_ context.Context, id string) error {
	d.stopped = append(d.stopped, id)
	d.running = false

	return nil
}

func (d *fakeDaemon) RemoveContainer(_ context.Context, id string) error {
	d.calls = append(d.calls, callRemove)
	d.removed = append(d.removed, id)

	return nil
}

func (d *fakeDaemon) InspectContainer(
	_ context.Context,
	id string,
) (docker.ContainerState, error) {
	if d.staleName != "" && id == d.staleName && !slices.Contains(d.removed, id) {
		return docker.ContainerState{
			ID:      id,
			Running: d.staleRunning,
			Labels:  d.staleLabels,
		}, nil
	}

	// Only the container this fake created exists. A launch looks its name up
	// first, and a name nobody holds is not found.
	if id != testContainerID {
		return docker.ContainerState{}, commerr.ErrNotFound
	}

	if d.inspectErr != nil {
		return docker.ContainerState{}, d.inspectErr
	}

	return docker.ContainerState{
		ID:      id,
		Running: d.running,
		ImageID: testLocalImageID,
		Labels:  d.labels,
	}, nil
}

// The launcher creates, feeds the document over stdin, then starts. The order
// matters: a container started before its document would have nothing to read.
func TestLaunchFeedsTheDocumentBeforeStarting(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	daemon := &fakeDaemon{
		labels:      worker.ContainerLabels(sessionID),
		repoDigests: []string{testImage},
	}

	launcher, err := docker.New(docker.Options{
		Client:   daemon,
		Identity: testIdentity(),
	})
	require.NoError(t, err)

	process, err := launcher.Launch(t.Context(), worker.LaunchRequest{
		Document:   testDocument(sessionID),
		AgentSpace: sandboxSpace(),
	})
	require.NoError(t, err)

	assert.Equal(t, []string{testContainerID}, daemon.started)
	require.NotEmpty(t, daemon.stdin, "the launch document must reach stdin")
	assert.Contains(t, string(daemon.stdin), testCredential)

	descriptor := process.Describe()
	assert.Equal(t, testContainerID, descriptor.ContainerID)

	// The recorded identity is the repository digest the daemon reported, not
	// the local image ID a container inspect returns.
	assert.Equal(t, testImage, descriptor.ImageDigest)
	assert.NotEqual(t, testLocalImageID, descriptor.ImageDigest)
}

// A controller that crashed left its worker container behind with the
// session's deterministic name, and every later launch failed on the name.
// The launch now removes the leftover when its labels show it is this
// session's worker, and refuses anything else holding the name.
func TestLaunchClearsTheSessionsStaleWorker(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	staleName := worker.ContainerName(sessionID)

	testCases := []struct {
		name        string
		labels      map[string]string
		running     bool
		wantErr     error
		wantRemoved []string
		wantStopped []string
	}{
		{
			name:        "stopped leftover of this session",
			labels:      worker.ContainerLabels(sessionID),
			wantRemoved: []string{staleName},
		},
		{
			name:        "running leftover of this session",
			labels:      worker.ContainerLabels(sessionID),
			running:     true,
			wantRemoved: []string{staleName},
			wantStopped: []string{staleName},
		},
		{
			name:    "another session's container",
			labels:  worker.ContainerLabels(uuid.New()),
			wantErr: commerr.ErrPermissionDenied,
		},
		{
			name:    "a container Peen does not manage",
			labels:  map[string]string{},
			wantErr: commerr.ErrPermissionDenied,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			daemon := &fakeDaemon{
				labels:       worker.ContainerLabels(sessionID),
				repoDigests:  []string{testImage},
				staleName:    staleName,
				staleLabels:  tc.labels,
				staleRunning: tc.running,
			}
			launcher, err := docker.New(docker.Options{
				Client:   daemon,
				Identity: testIdentity(),
			})
			require.NoError(t, err)

			_, err = launcher.Launch(t.Context(), worker.LaunchRequest{
				Document:   testDocument(sessionID),
				AgentSpace: sandboxSpace(),
			})

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Empty(t, daemon.removed)
				assert.Empty(t, daemon.started)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.wantRemoved, daemon.removed)
			assert.Equal(t, tc.wantStopped, daemon.stopped)
			assert.Equal(t, []string{testContainerID}, daemon.started)
		})
	}
}

// TestLaunchFollowsTheWorkerContainerLogs is the Docker half of the audit
// trail.
//
// A Docker worker never receives the audit directory, so it cannot write the
// durable trail itself. Without the controller following its output, every
// hook, skill, child agent, and tool record the worker produced stayed in the
// container and the audit file held controller records only.
func TestLaunchFollowsTheWorkerContainerLogs(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	daemon := &fakeDaemon{
		labels:      worker.ContainerLabels(sessionID),
		repoDigests: []string{testImage},
		logPayload:  `{"level":"INFO","msg":"skill activated"}` + "\n",
	}

	launcher, err := docker.New(docker.Options{
		Client:   daemon,
		Identity: testIdentity(),
	})
	require.NoError(t, err)

	_, err = launcher.Launch(t.Context(), worker.LaunchRequest{
		Document:   testDocument(sessionID),
		AgentSpace: sandboxSpace(),
	})
	require.NoError(t, err)

	assert.Equal(t, []string{testContainerID}, daemon.loggedContainers)
}

// A worker whose logs cannot be followed still runs. Losing the audit records
// is a degraded trail, not a reason to refuse a session its worker.
func TestLaunchSurvivesALogFollowFailure(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	daemon := &fakeDaemon{
		labels:      worker.ContainerLabels(sessionID),
		repoDigests: []string{testImage},
		logErr:      commerr.ErrFetchFailed,
	}

	launcher, err := docker.New(docker.Options{
		Client:   daemon,
		Identity: testIdentity(),
	})
	require.NoError(t, err)

	process, err := launcher.Launch(t.Context(), worker.LaunchRequest{
		Document:   testDocument(sessionID),
		AgentSpace: sandboxSpace(),
	})
	require.NoError(t, err)
	assert.Equal(t, testContainerID, process.Describe().ContainerID)
	assert.Equal(t, []string{testContainerID}, daemon.loggedContainers)
}

// A host that has never seen the worker image pulls it, because the Engine
// API's container create does not pull the way the docker CLI does.
func TestLaunchPullsAnAbsentImage(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	daemon := &fakeDaemon{
		labels:            worker.ContainerLabels(sessionID),
		repoDigests:       []string{testImage},
		absentUntilPulled: true,
	}

	launcher, err := docker.New(docker.Options{
		Client:   daemon,
		Identity: testIdentity(),
	})
	require.NoError(t, err)

	process, err := launcher.Launch(t.Context(), worker.LaunchRequest{
		Document:   testDocument(sessionID),
		AgentSpace: sandboxSpace(),
	})
	require.NoError(t, err)

	assert.Equal(t, []string{testImage}, daemon.pulled)
	assert.Equal(t, testImage, process.Describe().ImageDigest)
}

// A failed pull stops the launch, rather than leaving a container to be
// created from an image the daemon does not have.
func TestLaunchFailsWhenTheImageCannotBePulled(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	daemon := &fakeDaemon{
		labels:            worker.ContainerLabels(sessionID),
		repoDigests:       []string{testImage},
		absentUntilPulled: true,
		pullErr:           commerr.ErrFetchFailed,
	}

	launcher, err := docker.New(docker.Options{
		Client:   daemon,
		Identity: testIdentity(),
	})
	require.NoError(t, err)

	_, err = launcher.Launch(t.Context(), worker.LaunchRequest{
		Document:   testDocument(sessionID),
		AgentSpace: sandboxSpace(),
	})

	require.ErrorIs(t, err, commerr.ErrFetchFailed)
	assert.Empty(t, daemon.started)
}

// An image the daemon holds no repository digest for still launches. A locally
// built image has none until it is pushed, and the generation records no digest
// rather than the local image ID, which no registry could resolve.
func TestLaunchRecordsNoDigestForALocalImage(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	daemon := &fakeDaemon{labels: worker.ContainerLabels(sessionID)}

	launcher, err := docker.New(docker.Options{
		Client:        daemon,
		Identity:      testIdentity(),
		ImageOverride: "peen-dev:local",
	})
	require.NoError(t, err)

	process, err := launcher.Launch(t.Context(), worker.LaunchRequest{
		Document:   testDocument(sessionID),
		AgentSpace: sandboxSpace(),
	})
	require.NoError(t, err)

	assert.Equal(t, []string{testContainerID}, daemon.started)
	assert.Empty(t, process.Describe().ImageDigest)
	assert.NotEqual(t, testLocalImageID, process.Describe().ImageDigest)
}

// A repository digest belonging to some other repository is not this image's
// identity, so it is not recorded as one.
func TestLaunchIgnoresAForeignRepositoryDigest(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	daemon := &fakeDaemon{
		labels: worker.ContainerLabels(sessionID),
		repoDigests: []string{
			"someone-else/peen@sha256:" +
				"3333333333333333333333333333333333333333333333333333333333333333",
		},
	}

	launcher, err := docker.New(docker.Options{
		Client:   daemon,
		Identity: testIdentity(),
	})
	require.NoError(t, err)

	process, err := launcher.Launch(t.Context(), worker.LaunchRequest{
		Document:   testDocument(sessionID),
		AgentSpace: sandboxSpace(),
	})
	require.NoError(t, err)

	// The digest belongs to another repository, so it is not this image's
	// identity and nothing is recorded.
	assert.Empty(t, process.Describe().ImageDigest)
}

// A tag names the image, and the daemon's own repository digest for it is what
// gets recorded. The operator writes the tag their release published, and the
// generation still carries the exact bytes that ran.
func TestLaunchRecordsTheDigestBehindATag(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	daemon := &fakeDaemon{
		labels:      worker.ContainerLabels(sessionID),
		repoDigests: []string{testImage},
	}

	launcher, err := docker.New(docker.Options{
		Client:        daemon,
		Identity:      testIdentity(),
		ImageOverride: worker.ImageRepository + ":v1.2.3",
	})
	require.NoError(t, err)

	process, err := launcher.Launch(t.Context(), worker.LaunchRequest{
		Document:   testDocument(sessionID),
		AgentSpace: sandboxSpace(),
	})
	require.NoError(t, err)

	assert.Equal(t, []string{testContainerID}, daemon.started)
	assert.Equal(t, testImage, process.Describe().ImageDigest)
}

// An image from another repository launches like any other. The deployment
// naming it is naming it on purpose, and a controller that can create
// containers at all can already create them from any image.
func TestLaunchAcceptsAnotherRepository(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	daemon := &fakeDaemon{labels: worker.ContainerLabels(sessionID)}

	launcher, err := docker.New(docker.Options{
		Client:        daemon,
		Identity:      testIdentity(),
		ImageOverride: "someoneelse/peen:v1.2.3",
	})
	require.NoError(t, err)

	_, err = launcher.Launch(t.Context(), worker.LaunchRequest{
		Document:   testDocument(sessionID),
		AgentSpace: sandboxSpace(),
	})
	require.NoError(t, err)

	assert.Equal(t, []string{testContainerID}, daemon.started)
}

// A launch with no image anywhere is refused, because a container cannot be
// created without one.
func TestLaunchRefusesAnEmptyImage(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	daemon := &fakeDaemon{labels: worker.ContainerLabels(sessionID)}

	launcher, err := docker.New(docker.Options{
		Client:   daemon,
		Identity: testIdentity(),
	})
	require.NoError(t, err)

	space := sandboxSpace()
	space.Image = ""

	_, err = launcher.Launch(t.Context(), worker.LaunchRequest{
		Document:   testDocument(sessionID),
		AgentSpace: space,
	})

	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	assert.Empty(t, daemon.started)
}

// Stopping acts only on a container whose labels still mark it as this
// session's worker, so an unrelated container is never touched.
func TestStopRequiresTheOwnershipLabels(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()

	daemon := &fakeDaemon{
		labels: map[string]string{
			worker.LabelManaged: worker.LabelManagedValue,
			worker.LabelSession: uuid.NewString(),
		},
		repoDigests: []string{testImage},
	}

	launcher, err := docker.New(docker.Options{
		Client:   daemon,
		Identity: testIdentity(),
	})
	require.NoError(t, err)

	process, err := launcher.Launch(t.Context(), worker.LaunchRequest{
		Document:   testDocument(sessionID),
		AgentSpace: sandboxSpace(),
	})
	require.NoError(t, err)

	err = process.Stop(t.Context())

	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	assert.Empty(t, daemon.stopped, "a foreign container is never stopped")
	assert.Empty(t, daemon.removed, "a foreign container is never removed")
}

// Stopping this controller's own worker does stop and remove it.
func TestStopRemovesTheOwnedContainer(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	daemon := &fakeDaemon{
		labels:      worker.ContainerLabels(sessionID),
		repoDigests: []string{testImage},
	}

	launcher, err := docker.New(docker.Options{
		Client:   daemon,
		Identity: testIdentity(),
	})
	require.NoError(t, err)

	process, err := launcher.Launch(t.Context(), worker.LaunchRequest{
		Document:   testDocument(sessionID),
		AgentSpace: sandboxSpace(),
	})
	require.NoError(t, err)

	require.NoError(t, process.Stop(t.Context()))

	assert.Equal(t, []string{testContainerID}, daemon.stopped)
	assert.Equal(t, []string{testContainerID}, daemon.removed)
}

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

// A launcher without a daemon client cannot exist, which is what turns absent
// Docker authority into a startup decision rather than a per-launch surprise.
func TestNewRefusesWithoutADaemonClient(t *testing.T) {
	t.Parallel()

	_, err := docker.New(docker.Options{Identity: testIdentity()})

	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
}
