//go:build dockerworker

// Package dockerworker is the image-entrypoint contract test.
//
// It answers one question the daemon-free tests cannot: a worker container is
// created with User 0:0, so does the image's entrypoint actually drop to the
// controller's host account before anything else runs, and is the resulting
// process as confined as the profile claims.
//
// What it does NOT do: it runs no agent, no model, no provider, and no
// controller database. There is no worker protocol in this suite. The container
// keeps the real Peen entrypoint and replaces only the program that entrypoint
// finally execs, with a fixed probe that records what it can see and then
// exits. Everything about routing, turns, and durable state is covered
// elsewhere.
//
// Containment rules this file holds itself to:
//
//   - One disposable root per run, under the repository's gitignored .testing/
//     directory. The DIND Make target binds the repository at the identical
//     host path, so a bind source inside it resolves to the same bytes on both
//     sides. A path under /tmp would be the test container's /tmp, which the
//     host daemon would resolve to some other directory or create empty, and
//     every filesystem assertion below would be meaningless.
//   - Exactly one container, named for a session UUID this run generated.
//   - Teardown inspects that exact container ID and acts only when
//     worker.OwnedBy still reports it as this session's worker. No name
//     pattern, no label selector, no prune.
//   - It removes only the disposable root it created.
package dockerworker_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/psyb0t/peen/internal/pkg/worker/docker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// enableEnvKey is the explicit opt-in. The build tag alone is not enough: a
	// target that compiles this package should still refuse to create a
	// container unless the operator asked for one in this run.
	enableEnvKey = "PEEN_DOCKER_WORKER_TEST"

	// imageEnvKey names the pinned worker image. There is no default, because a
	// default would let this suite create a container from whatever the local
	// daemon happened to have under that name.
	imageEnvKey = "PEEN_DOCKER_WORKER_TEST_IMAGE"

	// localImageEnvKey is set only by make test-docker-worker-source. The
	// production profile still sees a pinned repository image, then the test
	// swaps only its probe request to the Dockerfile image it built itself.
	localImageEnvKey = "PEEN_DOCKER_WORKER_TEST_LOCAL_IMAGE"

	// socketEnvKey names the daemon socket. The controller's own socket is a
	// deliberate grant, so it is named rather than discovered.
	socketEnvKey = "PEEN_DOCKER_WORKER_TEST_SOCKET"

	enabledValue = "true"

	directoryMode = 0o700
	fileMode      = 0o600

	testProfileName  = "audit-sandbox"
	repositoryModule = "go.mod"
	testingDirectory = ".testing"
	rootPrefix       = "dockerworker-"

	dockerSocketPath = "/var/run/docker.sock"
	agentsFileName   = "AGENTS.md"
	agentsFileBody   = "Follow the entrypoint contract test rules.\n"

	// stateSecretName stands in for anything controller-owned under the state
	// directory. The state directory is never mounted, so the probe must not
	// find it.
	stateSecretName = "controller-only-secret"
	stateSecretBody = "this must never be readable from a worker\n"

	socketRootName    = "workers"
	socketSentinel    = "worker.sock"
	resultFileName    = "probe-result.json"
	ownedFileName     = "probe-owned-file"
	probeShell        = "/bin/bash"
	probeShellFlag    = "-c"
	tiniPath          = "/usr/bin/tini"
	tiniSeparator     = "--"
	peenEntrypoint    = "/usr/local/bin/peen-entrypoint"
	expectedRootUsers = "0:0"
	sourceImagePrefix = "peen-dockerworker-source-"

	// sourceContractProfileImage is a syntactically valid but deliberately
	// nonexistent repository image. The source-image target never asks Docker
	// to run it: it proves the normal builder accepts a pinned image, then
	// replaces only the test probe's image with the just-built local one.
	sourceContractProfileImage = "psyb0t/peen@sha256:" +
		"0000000000000000000000000000000000000000000000000000000000000000"

	// containerExitTimeout bounds the probe. It writes one file and exits, so a
	// container still running after this is a finding rather than slowness.
	containerExitTimeout = 90 * time.Second
	containerPollEvery   = 250 * time.Millisecond
	teardownTimeout      = 60 * time.Second

	probeResultEnvKey      = "PEEN_PROBE_RESULT"
	probeOwnedFileEnvKey   = "PEEN_PROBE_OWNED_FILE"
	probeStateSecretEnvKey = "PEEN_PROBE_STATE_SECRET"
	probeForeignSockEnvKey = "PEEN_PROBE_FOREIGN_SOCKET"
	probeSessionDirEnvKey  = "PEEN_PROBE_SESSION_SOCKET_DIR"
	probeSocketRootEnvKey  = "PEEN_PROBE_SOCKET_ROOT"
)

// probeScript is fixed text. Every path it touches arrives through the
// environment, so nothing from the test is interpolated into shell source.
//
// It runs after the Peen entrypoint has dropped privileges, records what it can
// observe, and writes one JSON object into the bind-mounted workspace. It does
// not use set -e, because most of the commands below are expected to fail and
// their failure is the result being recorded.
const probeScript = `
set -u

uid="$(id -u)"
gid="$(id -g)"
username="$(id -un)"

sudo_allowed=false
if sudo -n true >/dev/null 2>&1; then
    sudo_allowed=true
fi

docker_socket_present=false
if [ -e /var/run/docker.sock ]; then
    docker_socket_present=true
fi

config_readable=false
if [ -r "$PEEN_CONFIG_DIR/AGENTS.md" ]; then
    config_readable=true
fi

state_secret_visible=false
if [ -e "$PEEN_PROBE_STATE_SECRET" ]; then
    state_secret_visible=true
fi

foreign_socket_visible=false
if [ -e "$PEEN_PROBE_FOREIGN_SOCKET" ]; then
    foreign_socket_visible=true
fi

session_socket_visible=false
if [ -d "$PEEN_PROBE_SESSION_SOCKET_DIR" ]; then
    session_socket_visible=true
fi

socket_root_entries=0
if [ -d "$PEEN_PROBE_SOCKET_ROOT" ]; then
    socket_root_entries="$(ls -1 "$PEEN_PROBE_SOCKET_ROOT" 2>/dev/null | wc -l | tr -d ' ')"
fi

network_reachable=false
if timeout 5 bash -c 'exec 3<>/dev/tcp/1.1.1.1/443' >/dev/null 2>&1; then
    network_reachable=true
fi

touch "$PEEN_PROBE_OWNED_FILE"

printf '{"uid":%s,"gid":%s,"username":"%s","sudoAllowed":%s,"dockerSocketPresent":%s,"configReadable":%s,"stateSecretVisible":%s,"foreignSocketVisible":%s,"sessionSocketVisible":%s,"socketRootEntries":%s,"networkReachable":%s}\n' \
    "$uid" "$gid" "$username" \
    "$sudo_allowed" "$docker_socket_present" "$config_readable" \
    "$state_secret_visible" "$foreign_socket_visible" \
    "$session_socket_visible" "$socket_root_entries" "$network_reachable" \
    > "$PEEN_PROBE_RESULT"
`

// probeResult is what the container reports about itself.
type probeResult struct {
	UID                  int    `json:"uid"`
	GID                  int    `json:"gid"`
	Username             string `json:"username"`
	SudoAllowed          bool   `json:"sudoAllowed"`
	DockerSocketPresent  bool   `json:"dockerSocketPresent"`
	ConfigReadable       bool   `json:"configReadable"`
	StateSecretVisible   bool   `json:"stateSecretVisible"`
	ForeignSocketVisible bool   `json:"foreignSocketVisible"`
	SessionSocketVisible bool   `json:"sessionSocketVisible"`
	SocketRootEntries    int    `json:"socketRootEntries"`
	NetworkReachable     bool   `json:"networkReachable"`
}

// disposableRoot is one run's entire filesystem footprint, all of it inside the
// repository so a bind source resolves to the same bytes on host and daemon.
type disposableRoot struct {
	root        string
	config      string
	state       string
	workspace   string
	socketRoot  string
	stateSecret string

	// foreignSocketPath is a second session's socket under the shared root. The
	// probe must not be able to see it.
	foreignSocketPath string
}

// TestDockerWorkerEntrypointDropsToTheHostAccount creates exactly one container
// and asserts what the dropped process can and cannot do.
func TestDockerWorkerEntrypointDropsToTheHostAccount(t *testing.T) {
	requireOptIn(t)

	identity := requireNonRootIdentity(t)
	paths := newDisposableRoot(t)
	sessionID := uuid.New()
	foreignSessionID := uuid.New()

	paths.writeForeignSocketSentinel(t, foreignSessionID)

	client, err := docker.NewDaemonClient(daemonSocket(t))
	require.NoError(t, err)

	// The request comes from the production builder, so the user, environment,
	// mounts, and security options under test are the real ones.
	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document: paths.launchDocument(t, sessionID),
		Profile:  sealedProfile(t),
		Identity: identity,
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

	result := paths.readProbeResult(t)

	assert.Equal(t, identity.UID, result.UID, "the probe must run as the host UID")
	assert.Equal(t, identity.GID, result.GID, "the probe must run as the host GID")
	assert.Equal(t, identity.Username, result.Username)

	assert.False(t, result.SudoAllowed, "an ordinary profile grants no sudo")
	assert.False(t, result.DockerSocketPresent, "no Docker socket is mounted")
	assert.False(t, result.NetworkReachable, "the network is disabled")

	assert.True(t, result.ConfigReadable, "the configuration mount is readable")
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
	assert.True(t, create.NetworkDisabled)
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
		[]string{paths.workspace, paths.config, sessionSocketDir},
		targets,
		"a sealed worker receives exactly three mounts",
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

	return create
}

// registerOwnedTeardown stops and removes the one container this test created,
// and only while the daemon still reports it as this session's worker.
//
// It mirrors the launcher's own stop rule through the exported ownership
// predicate. The launcher's Process cannot be used directly here, because the
// probe replaces the worker command that Launch would have run.
func registerOwnedTeardown(
	t *testing.T,
	client docker.Client,
	containerID string,
	sessionID uuid.UUID,
) {
	t.Helper()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(
			context.WithoutCancel(t.Context()),
			teardownTimeout,
		)
		defer cancel()

		state, err := client.InspectContainer(ctx, containerID)
		if !assert.NoError(t, err) {
			return
		}

		if !assert.True(
			t,
			worker.OwnedBy(state.Labels, sessionID),
			"refusing to touch a container that is not this test's worker",
		) {
			return
		}

		if state.Running {
			assert.NoError(t, client.StopContainer(ctx, containerID))
		}

		assert.NoError(t, client.RemoveContainer(ctx, containerID))
	})
}

// awaitContainerExit polls the daemon rather than sleeping, so a probe that
// finishes early does not cost the suite its whole budget.
func awaitContainerExit(
	t *testing.T,
	client docker.Client,
	containerID string,
) {
	t.Helper()

	require.Eventually(
		t,
		func() bool {
			state, err := client.InspectContainer(t.Context(), containerID)
			if err != nil {
				return false
			}

			return !state.Running
		},
		containerExitTimeout,
		containerPollEvery,
		"the probe container did not exit",
	)
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

	assert.Equal(t, uint32(identity.UID), stat.Uid)
	assert.Equal(t, uint32(identity.GID), stat.Gid)
}

// guardedClient answers image inspection from fixed data and fails the test if
// anything ever reaches container creation.
type guardedClient struct {
	t           *testing.T
	repoDigests []string
	inspectErr  error
}

func (g *guardedClient) InspectImage(
	_ context.Context,
	_ string,
) (docker.ImageIdentity, error) {
	if g.inspectErr != nil {
		return docker.ImageIdentity{}, g.inspectErr
	}

	return docker.ImageIdentity{RepoDigests: g.repoDigests}, nil
}

func (g *guardedClient) CreateContainer(
	_ context.Context,
	_ docker.CreateRequest,
) (string, error) {
	g.t.Fatal("a refused launch must never create a container")

	return "", nil
}

func (g *guardedClient) AttachStdin(
	_ context.Context,
	_ string,
	_ []byte,
) error {
	g.t.Fatal("a refused launch must never write a credential")

	return nil
}

func (g *guardedClient) StartContainer(_ context.Context, _ string) error {
	g.t.Fatal("a refused launch must never start a container")

	return nil
}

func (g *guardedClient) StopContainer(_ context.Context, _ string) error {
	return nil
}

func (g *guardedClient) RemoveContainer(_ context.Context, _ string) error {
	return nil
}

func (g *guardedClient) InspectContainer(
	_ context.Context,
	_ string,
) (docker.ContainerState, error) {
	return docker.ContainerState{}, nil
}

// sealedProfile is the only profile this suite ever launches: no Docker socket,
// no sudo, no network, no extra mounts.
func sealedProfile(t *testing.T) worker.Profile {
	t.Helper()

	profile := worker.Profile{
		Name:                     testProfileName,
		Kind:                     worker.KindDocker,
		Revision:                 1,
		Image:                    workerImage(t),
		AllowDockerSocket:        false,
		AllowNetwork:             false,
		AllowPrivilegeEscalation: false,
	}

	require.NoError(t, profile.Validate())
	require.Empty(t, profile.Mounts, "the sealed profile adds no mounts")

	return profile
}

// newDisposableRoot builds this run's whole filesystem footprint inside the
// repository's gitignored .testing/ directory.
//
// It is not t.TempDir() and not os.MkdirTemp(""). Those land in the test
// container's /tmp, which the host daemon would resolve somewhere else
// entirely, and every mount assertion in this file would then be inspecting a
// daemon-created empty directory rather than the one the test wrote to.
func newDisposableRoot(t *testing.T) disposableRoot {
	t.Helper()

	root := filepath.Join(
		repositoryPath(t, testingDirectory),
		rootPrefix+uuid.NewString(),
	)

	paths := disposableRoot{
		root:      root,
		config:    filepath.Join(root, "config"),
		state:     filepath.Join(root, "state"),
		workspace: filepath.Join(root, "workspace"),
	}
	paths.socketRoot = filepath.Join(paths.state, socketRootName)
	paths.stateSecret = filepath.Join(paths.state, stateSecretName)

	for _, directory := range []string{
		paths.config,
		paths.state,
		paths.workspace,
		paths.socketRoot,
	} {
		require.NoError(t, os.MkdirAll(directory, directoryMode))
	}

	require.NoError(t, os.WriteFile(
		filepath.Join(paths.config, agentsFileName),
		[]byte(agentsFileBody),
		fileMode,
	))

	// The probe must not be able to read this. It stands in for peen.db and the
	// audit logs, which live in the same directory.
	require.NoError(t, os.WriteFile(
		paths.stateSecret,
		[]byte(stateSecretBody),
		fileMode,
	))

	// Only the root this test created is removed.
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })

	return paths
}

// writeForeignSocketSentinel plants a second session's socket directory under
// the shared socket root. The probe must not be able to see it, which is what
// proves a worker receives its own directory rather than the root.
func (d *disposableRoot) writeForeignSocketSentinel(
	t *testing.T,
	foreignSessionID uuid.UUID,
) {
	t.Helper()

	directory := worker.SessionSocketDirectory(d.socketRoot, foreignSessionID)
	require.NoError(t, os.MkdirAll(directory, directoryMode))

	sentinel := filepath.Join(directory, socketSentinel)
	require.NoError(t, os.WriteFile(sentinel, nil, fileMode))

	d.foreignSocketPath = sentinel
}

func (d disposableRoot) resultPath() string {
	return filepath.Join(d.workspace, resultFileName)
}

func (d disposableRoot) ownedFilePath() string {
	return filepath.Join(d.workspace, ownedFileName)
}

func (d disposableRoot) readProbeResult(t *testing.T) probeResult {
	t.Helper()

	raw, err := os.ReadFile(d.resultPath())
	require.NoError(t, err, "the probe wrote no result file")

	result := probeResult{}
	require.NoError(t, json.Unmarshal(raw, &result))

	return result
}

func (d disposableRoot) launchDocument(
	t *testing.T,
	sessionID uuid.UUID,
) worker.LaunchDocument {
	t.Helper()

	credential, err := worker.NewCredential()
	require.NoError(t, err)

	document := worker.LaunchDocument{
		SessionID:       sessionID,
		GenerationID:    uuid.New(),
		Credential:      credential,
		SocketPath:      worker.SocketPathFor(d.socketRoot, sessionID),
		Workspace:       d.workspace,
		ConfigDirectory: d.config,
		Profile:         testProfileName,
	}
	require.NoError(t, document.Validate())

	// The session's own socket directory has to exist before the container
	// mounts it, and it is the only part of the socket root the worker sees.
	require.NoError(t, os.MkdirAll(
		worker.SessionSocketDirectory(d.socketRoot, sessionID),
		directoryMode,
	))

	return document
}

// requireOptIn fails rather than skips. A silent skip on a suite whose whole
// purpose is creating a container hides the fact that nothing ran.
func requireOptIn(t *testing.T) {
	t.Helper()

	if os.Getenv(enableEnvKey) != enabledValue {
		t.Fatalf(
			"%s must be %q to run the Docker worker entrypoint contract test",
			enableEnvKey,
			enabledValue,
		)
	}
}

// requireNonRootIdentity is the account the entrypoint drops to. The launcher
// refuses a root identity, so this states the requirement up front.
func requireNonRootIdentity(t *testing.T) docker.HostIdentity {
	t.Helper()

	// The DIND test runner deliberately runs under the host UID and GID without
	// adding that UID to its own passwd file. This is the same shape as a
	// controller launched with `docker run --user`, so this exercises the
	// explicit host identity a production Docker controller must provide.
	identity, err := docker.CurrentHostIdentityWithOverride(
		"peen-dockerworker-test",
		"/home/peen-dockerworker-test",
	)
	require.NoError(t, err)
	require.NoError(
		t,
		identity.Validate(),
		"run this suite as an ordinary non-root account",
	)

	return identity
}

func workerImage(t *testing.T) string {
	t.Helper()

	if sourceWorkerImage(t) != "" {
		return sourceContractProfileImage
	}

	image := strings.TrimSpace(os.Getenv(imageEnvKey))
	require.NotEmpty(
		t,
		image,
		"%s must name a published %s image",
		imageEnvKey,
		worker.ImageRepository,
	)

	return image
}

func sourceWorkerImage(t *testing.T) string {
	t.Helper()

	image := strings.TrimSpace(os.Getenv(localImageEnvKey))
	if image == "" {
		return ""
	}

	require.True(
		t,
		strings.HasPrefix(image, sourceImagePrefix),
		"%s must be a temporary image built by test-docker-worker-source",
		localImageEnvKey,
	)

	return image
}

func daemonSocket(t *testing.T) string {
	t.Helper()

	socket := strings.TrimSpace(os.Getenv(socketEnvKey))
	if socket == "" {
		socket = dockerSocketPath
	}

	info, err := os.Stat(socket)
	require.NoError(t, err, "the controller Docker socket must be reachable")
	require.NotZero(
		t,
		info.Mode()&os.ModeSocket,
		"%s is not a socket",
		socket,
	)

	return socket
}

// repositoryPath resolves a path from the repository root, found by walking up
// from the package directory to the module file.
func repositoryPath(t *testing.T, relative string) string {
	t.Helper()

	directory, err := os.Getwd()
	require.NoError(t, err)

	for {
		if _, err := os.Stat(
			filepath.Join(directory, repositoryModule),
		); err == nil {
			return filepath.Join(directory, relative)
		}

		parent := filepath.Dir(directory)
		require.NotEqual(t, parent, directory, "repository root not found")

		directory = parent
	}
}

func isInside(ancestor, candidate string) bool {
	return strings.HasPrefix(
		candidate,
		ancestor+string(filepath.Separator),
	)
}
