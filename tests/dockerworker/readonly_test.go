//go:build dockerworker

package dockerworker_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/psyb0t/peen/internal/pkg/worker/docker"
	"github.com/psyb0t/peen/internal/pkg/worker/dockerproxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDockerWorkerReadonlyReachesOnlyTheProxy creates one `docker: readonly`
// container and proves, from inside it, that DOCKER_HOST reaches the real
// daemon through the proxy for reads, that a write is refused, that inspect
// output carries no environment values, and that no socket is mounted.
func TestDockerWorkerReadonlyReachesOnlyTheProxy(t *testing.T) {
	requireOptIn(t)

	identity := requireNonRootIdentity(t)
	paths := newDisposableRoot(t)
	sessionID := uuid.New()
	daemonSocketPath := daemonSocket(t)

	client, err := docker.NewDaemonClient(daemonSocketPath)
	require.NoError(t, err)

	// The controller refuses a socket root this deep at startup. Checking it
	// here names the cause instead of failing the proxy bind below.
	require.NoError(
		t,
		worker.ValidateSocketRoot(paths.socketRoot),
		"the checkout path is too deep for a session socket under .testing/",
	)

	document := paths.launchDocument(t, sessionID)

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document:         document,
		AgentSpace:       readonlySpace(t),
		Identity:         identity,
		DockerSocketPath: daemonSocketPath,
		DockerSocketGID:  docker.SocketGID(daemonSocketPath),
	})
	require.NoError(t, err)

	proxySocket := docker.ReadonlyProxySocketPath(document)
	assertReadonlyRequest(t, create, paths, sessionID, proxySocket)

	// The controller starts this proxy in Launch. The probe replaces the
	// worker command that Launch would run, so the test starts it the same
	// way, before the container exists.
	proxy, err := dockerproxy.Start(t.Context(), proxySocket, daemonSocketPath)
	require.NoError(t, err)

	t.Cleanup(func() { assert.NoError(t, proxy.Close()) })

	probe := paths.readonlyProbeRequest(t, create)

	containerID, err := client.CreateContainer(t.Context(), probe)
	require.NoError(t, err)
	require.NotEmpty(t, containerID)

	registerOwnedTeardown(t, client, containerID, sessionID)

	require.NoError(t, client.AttachStdin(t.Context(), containerID, nil))
	require.NoError(t, client.StartContainer(t.Context(), containerID))

	awaitContainerExit(t, client, containerID)

	result := readonlyProbeResult{}
	readJSONFile(t, paths.resultPath(), &result)

	assert.Equal(t, identity.UID, result.UID, "the probe must run as the host UID")
	assert.Equal(t, dockerHostScheme+proxySocket, result.DockerHost)
	assert.False(t, result.DockerSocketPresent, "no Docker socket is mounted")

	assert.Equal(t, httpStatusOK, result.PingStatus, "ping goes through")
	assert.Equal(t, httpStatusOK, result.ListStatus, "listing goes through")
	assert.Equal(t, httpStatusOK, result.InspectStatus, "inspect goes through")
	assert.Equal(
		t,
		httpStatusForbidden,
		result.CreateStatus,
		"creating a container must be refused",
	)

	containers := []map[string]any{}
	readJSONFile(
		t,
		filepath.Join(paths.workspace, listBodyFileName),
		&containers,
	)
	assert.NotEmpty(t, containers, "the list includes at least the probe")

	refusal := map[string]string{}
	readJSONFile(
		t,
		filepath.Join(paths.workspace, createBodyFileName),
		&refusal,
	)
	assert.True(
		t,
		strings.HasPrefix(refusal["message"], dockerproxy.RefusalPrefix),
		"the refusal names Peen's readonly access, got %q",
		refusal["message"],
	)

	assertRedactedInspect(t, paths)
}

// assertReadonlyRequest checks that the production request gives a readonly
// worker its proxy through DOCKER_HOST and nothing else: no socket mount, no
// socket group, no sudo.
func assertReadonlyRequest(
	t *testing.T,
	create docker.CreateRequest,
	paths disposableRoot,
	sessionID uuid.UUID,
	proxySocket string,
) {
	t.Helper()

	sessionSocketDir := worker.SessionSocketDirectory(
		paths.socketRoot,
		sessionID,
	)

	assert.Equal(t, sessionSocketDir, filepath.Dir(proxySocket))
	assert.Equal(t, dockerHostScheme+proxySocket, create.Env[dockerHostEnvKey])
	assert.Empty(t, create.Groups, "readonly needs no socket group")
	assert.True(t, create.NoNewPrivileges)
	assert.NotContains(t, create.Env, "PEEN_WORKER_ALLOW_SUDO")

	targets := make([]string, 0, len(create.Mounts))
	for _, mount := range create.Mounts {
		targets = append(targets, mount.Target)
	}

	assert.ElementsMatch(
		t,
		[]string{paths.workspace, paths.config, sessionSocketDir},
		targets,
		"a readonly worker receives the same three mounts as a sealed one",
	)
}

// assertRedactedInspect reads the probe container's inspect of itself. Its
// environment holds DOCKER_HOST and the probe paths, and every value must be
// redacted while the names stay.
func assertRedactedInspect(t *testing.T, paths disposableRoot) {
	t.Helper()

	inspect := struct {
		Config struct {
			Env []string `json:"Env"` //nolint:tagliatelle // Docker field name.
		} `json:"Config"` //nolint:tagliatelle // Docker field name.
	}{}
	readJSONFile(
		t,
		filepath.Join(paths.workspace, inspectBodyFileName),
		&inspect,
	)

	require.NotEmpty(t, inspect.Config.Env)
	assert.Contains(
		t,
		inspect.Config.Env,
		dockerHostEnvKey+redactedEnvSuffix,
	)

	for _, entry := range inspect.Config.Env {
		if !strings.Contains(entry, "=") {
			continue
		}

		assert.True(
			t,
			strings.HasSuffix(entry, redactedEnvSuffix),
			"inspect leaked an environment value in %q",
			entry,
		)
	}
}

// readonlyProbeRequest keeps the production request and replaces only the
// program the Peen entrypoint finally execs, as probeRequest does.
func (d disposableRoot) readonlyProbeRequest(
	t *testing.T,
	create docker.CreateRequest,
) docker.CreateRequest {
	t.Helper()

	create.Entrypoint = []string{
		tiniPath,
		tiniSeparator,
		peenEntrypoint,
		probeShell,
	}
	create.Command = []string{probeShellFlag, readonlyProbeScript}

	if image := sourceWorkerImage(t); image != "" {
		create.Image = image
	}

	create.Env[probeResultEnvKey] = d.resultPath()
	create.Env[probeListBodyEnvKey] = filepath.Join(
		d.workspace,
		listBodyFileName,
	)
	create.Env[probeInspectBodyEnvKey] = filepath.Join(
		d.workspace,
		inspectBodyFileName,
	)
	create.Env[probeCreateBodyEnvKey] = filepath.Join(
		d.workspace,
		createBodyFileName,
	)

	return create
}

// readonlySpace is the sealed space with `docker: readonly` and nothing else.
func readonlySpace(t *testing.T) worker.AgentSpace {
	t.Helper()

	space := sealedSpace(t)
	space.Docker = worker.DockerAccessReadonly

	require.NoError(t, space.Validate())

	return space
}

// readJSONFile decodes one file the probe wrote into the workspace.
func readJSONFile(t *testing.T, path string, target any) {
	t.Helper()

	raw, err := os.ReadFile(path)
	require.NoError(t, err, "the probe wrote no %s", filepath.Base(path))
	require.NoError(t, json.Unmarshal(raw, target))
}
