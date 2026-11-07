//go:build dockerworker

package dockerworker_test

import (
	"os"
	"syscall"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/psyb0t/peen/internal/pkg/worker/docker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDockerWorkerEntrypointDropsToTheHostAccount creates exactly one container
// and asserts what the dropped process can and cannot do.
func TestDockerWorkerEntrypointDropsToTheHostAccount(t *testing.T) {
	requireOptIn(t)

	identity := requireNonRootIdentity(t)
	paths, result := runEntrypointProbe(t, identity, false)

	assert.Equal(t, identity.UID, result.UID, "the probe must run as the host UID")
	assert.Equal(t, identity.GID, result.GID, "the probe must run as the host GID")
	assert.Equal(t, identity.Username, result.Username)

	assert.False(t, result.SudoAllowed, "an ordinary profile grants no sudo")
	assert.False(t, result.DockerSocketPresent, "no Docker socket is mounted")
	assert.False(t, result.NetworkReachable, "the network is disabled")

	assert.True(t, result.ConfigReadable, "the configuration mount is readable")
	assert.False(t, result.ConfigWritable, "the configuration is read-only")
	assert.True(t, result.LearnedReadable,
		"an auto-evolving worker reads the global learned directory")
	assert.False(t, result.LearnedWritable,
		"the global learned directory is read-only without the opt-in")
	assert.False(
		t,
		result.StateSecretVisible,
		"controller state must not be reachable from a worker",
	)

	assert.True(
		t,
		result.SessionSocketVisible,
		"this session's own socket directory must be mounted",
	)
	assert.False(
		t,
		result.ForeignSocketVisible,
		"another session's socket must not be reachable",
	)
	assert.Equal(
		t,
		1,
		result.SocketRootEntries,
		"the worker must see only its own session under the socket root",
	)

	assertHostOwnership(t, paths.ownedFilePath(), identity)
}

// TestDockerWorkerWritableLearnedDirectory proves that the operator's
// PEEN_WRITABLE_LEARNED_DIR opt-in makes only the global learned directory
// writable, even though it sits inside the read-only config directory.
func TestDockerWorkerWritableLearnedDirectory(t *testing.T) {
	requireOptIn(t)

	identity := requireNonRootIdentity(t)
	_, result := runEntrypointProbe(t, identity, true)

	assert.True(t, result.LearnedWritable,
		"the opted-in learned directory is writable")
	assert.False(t, result.ConfigWritable,
		"the rest of the configuration stays read-only")
}

// runEntrypointProbe creates exactly one container from the production
// request for an auto-evolving sealed worker, runs the probe in it, and
// returns what the dropped process reported.
func runEntrypointProbe(
	t *testing.T,
	identity docker.HostIdentity,
	writableLearned bool,
) (disposableRoot, probeResult) {
	t.Helper()

	paths := newDisposableRoot(t)
	sessionID := uuid.New()
	foreignSessionID := uuid.New()

	paths.writeForeignSocketSentinel(t, foreignSessionID)

	client, err := docker.NewDaemonClient(daemonSocket(t))
	require.NoError(t, err)

	document := paths.launchDocument(t, sessionID)
	document.LearnedDirectory = paths.learned
	document.WritableLearnedDirectory = writableLearned

	// The request comes from the production builder, so the user, environment,
	// mounts, and security options under test are the real ones.
	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document:   document,
		AgentSpace: sealedSpace(t),
		Identity:   identity,
	})
	require.NoError(t, err)

	assertSealedRequest(t, create, paths, sessionID)

	probe := paths.probeRequest(t, create, sessionID)

	containerID, err := client.CreateContainer(t.Context(), probe)
	require.NoError(t, err)
	require.NotEmpty(t, containerID)

	registerOwnedTeardown(t, client, containerID, sessionID)

	// The request is created with stdin open, matching a real worker launch.
	// Closing it immediately gives the probe an EOF it never reads.
	require.NoError(t, client.AttachStdin(t.Context(), containerID, nil))
	require.NoError(t, client.StartContainer(t.Context(), containerID))

	awaitContainerExit(t, client, containerID)

	return paths, paths.readProbeResult(t)
}

// assertSealedRequest checks the production request before anything is created,
// so a profile that quietly gained a capability fails here rather than inside a
// running container.
func assertSealedRequest(
	t *testing.T,
	create docker.CreateRequest,
	paths disposableRoot,
	sessionID uuid.UUID,
) {
	t.Helper()

	assert.Equal(t, expectedRootUsers, create.User)
	assert.True(t, create.NoNewPrivileges)
	assert.Equal(t, worker.NetworkModeNone, create.NetworkMode)
	assert.Empty(t, create.Groups)
	assert.Equal(t, worker.ContainerLabels(sessionID), create.Labels)
	assert.NotContains(t, create.Env, "PEEN_WORKER_ALLOW_SUDO")

	sessionSocketDir := worker.SessionSocketDirectory(
		paths.socketRoot,
		sessionID,
	)

	targets := make([]string, 0, len(create.Mounts))

	for _, mount := range create.Mounts {
		targets = append(targets, mount.Target)

		assert.NotEqual(t, dockerSocketPath, mount.Target)
		assert.NotEqual(
			t,
			paths.socketRoot,
			mount.Target,
			"the shared socket root must not be mounted",
		)
		assert.NotEqual(
			t,
			paths.state,
			mount.Target,
			"the state directory must not be mounted",
		)
		assert.False(
			t,
			isInside(paths.state, mount.Target) &&
				!isInside(sessionSocketDir, mount.Target) &&
				mount.Target != sessionSocketDir,
			"only this session's socket directory may come from state",
		)
	}

	assert.ElementsMatch(
		t,
		[]string{
			paths.workspace,
			paths.config,
			paths.learned,
			sessionSocketDir,
		},
		targets,
		"a sealed auto-evolving worker receives exactly four mounts",
	)
}

// probeRequest keeps the production request and replaces only the program the
// Peen entrypoint finally execs.
func (d disposableRoot) probeRequest(
	t *testing.T,
	create docker.CreateRequest,
	sessionID uuid.UUID,
) docker.CreateRequest {
	t.Helper()

	// The image entrypoint stays in front, so the privilege drop under test
	// still happens. Only the final program changes.
	create.Entrypoint = []string{
		tiniPath,
		tiniSeparator,
		peenEntrypoint,
		probeShell,
	}
	create.Command = []string{probeShellFlag, probeScript}

	if image := sourceWorkerImage(t); image != "" {
		create.Image = image
	}

	create.Env[probeResultEnvKey] = d.resultPath()
	create.Env[probeOwnedFileEnvKey] = d.ownedFilePath()
	create.Env[probeStateSecretEnvKey] = d.stateSecret
	create.Env[probeSocketRootEnvKey] = d.socketRoot
	create.Env[probeSessionDirEnvKey] = worker.SessionSocketDirectory(
		d.socketRoot,
		sessionID,
	)
	create.Env[probeForeignSockEnvKey] = d.foreignSocketPath
	create.Env[probeLearnedDirEnvKey] = d.learned
	create.Env[probeLearnedRuleEnvKey] = d.learnedRule()

	return create
}

// assertHostOwnership proves a file the probe created carries the controller's
// host identity, which is what keeps a mounted workspace manageable from the
// host after a worker has written to it.
func assertHostOwnership(
	t *testing.T,
	path string,
	identity docker.HostIdentity,
) {
	t.Helper()

	info, err := os.Stat(path)
	require.NoError(t, err, "the probe must create a file in the workspace")

	stat, ok := info.Sys().(*syscall.Stat_t)
	require.True(t, ok, "ownership is not readable on this platform")

	assert.EqualValues(t, identity.UID, stat.Uid)
	assert.EqualValues(t, identity.GID, stat.Gid)
}

// sealedSpace is the only agent space this suite ever launches: no Docker
// socket, no sudo, no network, no extra mounts.
func sealedSpace(t *testing.T) worker.AgentSpace {
	t.Helper()

	space := worker.AgentSpace{
		Name:     worker.DockerSpaceName(testProfileName),
		Kind:     worker.KindDocker,
		Revision: 1,
		Image:    workerImage(t),
		Docker:   worker.DockerAccessNone,
		Network:  worker.NetworkModeNone,
		Sudo:     false,
	}

	require.NoError(t, space.Validate())
	require.Empty(t, space.Mounts, "the sealed space adds no mounts")

	return space
}
