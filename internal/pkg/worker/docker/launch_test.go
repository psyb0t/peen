package docker_test

import (
	"context"
	"io"
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
		Profile:         worker.ProfileDockerSandbox,
	}
}

func sandboxProfile() worker.Profile {
	return worker.Profile{
		Name:     worker.ProfileDockerSandbox,
		Kind:     worker.KindDocker,
		Revision: 1,
		Image:    testImage,
	}
}

// A worker container is named for its session and carries exactly the two
// ownership labels, so cleanup can recognise it and nothing else.
func TestBuildCreateRequestUsesDeterministicNameAndLabels(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document: testDocument(sessionID),
		Profile:  sandboxProfile(),
		Identity: testIdentity(),
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

// The container runs the same worker subcommand a native worker runs, from the
// operator's pinned image.
func TestBuildCreateRequestRunsTheWorkerCommand(t *testing.T) {
	t.Parallel()

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document: testDocument(uuid.New()),
		Profile:  sandboxProfile(),
		Identity: testIdentity(),
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
		Document: testDocument(uuid.New()),
		Profile:  sandboxProfile(),
		Identity: testIdentity(),
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

// A Docker worker needs its model and provider settings just like a native
// worker, but controller state and the public control token must never cross
// the process boundary.
func TestBuildCreateRequestCarriesOnlyWorkerRuntimeEnvironment(t *testing.T) {
	t.Parallel()

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document: testDocument(uuid.New()),
		Profile:  sandboxProfile(),
		Identity: testIdentity(),
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
		"HOME",
	} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			_, err := docker.BuildCreateRequest(docker.LaunchSpec{
				Document: testDocument(uuid.New()),
				Profile:  sandboxProfile(),
				Identity: testIdentity(),
				RuntimeEnvironment: map[string]string{
					key: "must-not-cross",
				},
			})

			require.ErrorIs(t, err, commerr.ErrPermissionDenied)
		})
	}
}

// The workspace, configuration, and session socket directory keep their literal
// host paths, so a path in a prompt, a hook, or a log means the same thing on
// both sides.
func TestBuildCreateRequestKeepsLiteralPaths(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document: testDocument(sessionID),
		Profile:  sandboxProfile(),
		Identity: testIdentity(),
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
		Document: testDocument(sessionID),
		Profile:  sandboxProfile(),
		Identity: testIdentity(),
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
		Document: testDocument(uuid.New()),
		Profile:  sandboxProfile(),
		Identity: testIdentity(),
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
				Document: testDocument(uuid.New()),
				Profile:  sandboxProfile(),
				Identity: tc.identity,
			})

			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

// A sandbox profile gets no Docker socket and an isolated network. The worker
// socket is a separate, opt-in grant only a host-like profile carries.
func TestBuildCreateRequestKeepsTheSandboxSealed(t *testing.T) {
	t.Parallel()

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document: testDocument(uuid.New()),
		Profile:  sandboxProfile(),
		Identity: testIdentity(),
	})
	require.NoError(t, err)

	assert.True(t, create.NetworkDisabled)
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

// A host-like profile receives the socket it declares, plus the group needed
// to use it.
func TestBuildCreateRequestGrantsTheDeclaredDockerSocket(t *testing.T) {
	t.Parallel()

	profile := worker.Profile{
		Name:              worker.ProfileDockerHostLike,
		Kind:              worker.KindDocker,
		Revision:          1,
		Image:             testImage,
		AllowDockerSocket: true,
		AllowNetwork:      true,
	}

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document:        testDocument(uuid.New()),
		Profile:         profile,
		Identity:        testIdentity(),
		DockerSocketGID: testDockerGID,
	})
	require.NoError(t, err)

	assert.False(t, create.NetworkDisabled)
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

// Every Docker worker gets a root-only identity bootstrap. Without the explicit
// escalation grant, the entrypoint writes no sudoers rule and the daemon sets
// no-new-privileges for the worker after the drop.
func TestBuildCreateRequestWithoutPrivilegeEscalation(t *testing.T) {
	t.Parallel()

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document: testDocument(uuid.New()),
		Profile:  sandboxProfile(),
		Identity: testIdentity(),
	})
	require.NoError(t, err)

	assert.Equal(t, "0:0", create.User)
	assert.NotContains(t, create.Env, "PEEN_WORKER_ALLOW_SUDO")
	assert.Equal(t, "1000", create.Env["PEEN_WORKER_UID"])
	assert.Equal(t, "peen", create.Env["PEEN_WORKER_USERNAME"])
	assert.True(t, create.NoNewPrivileges)
}

// A profile the operator marked allowPrivilegeEscalation starts as root so the
// image entrypoint can build the host account and write its sudoers rule. The
// identity it hands that entrypoint is the controller's own, so the agent still
// ends up running as the host user rather than as root.
func TestBuildCreateRequestWithPrivilegeEscalation(t *testing.T) {
	t.Parallel()

	profile := worker.Profile{
		Name:                     worker.ProfileDockerHostLike,
		Kind:                     worker.KindDocker,
		Revision:                 1,
		Image:                    testImage,
		AllowNetwork:             true,
		AllowPrivilegeEscalation: true,
	}

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document: testDocument(uuid.New()),
		Profile:  profile,
		Identity: testIdentity(),
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

	// Escalation alone grants no socket. That is a separate opt-in.
	for _, mount := range create.Mounts {
		assert.NotEqual(t, dockerSocketPath, mount.Target)
	}
}

// The two grants compose without either implying the other, and the socket
// group reaches the entrypoint so the account it creates can use the socket.
func TestBuildCreateRequestWithEscalationAndDockerSocket(t *testing.T) {
	t.Parallel()

	profile := worker.Profile{
		Name:                     worker.ProfileDockerHostLike,
		Kind:                     worker.KindDocker,
		Revision:                 1,
		Image:                    testImage,
		AllowDockerSocket:        true,
		AllowNetwork:             true,
		AllowPrivilegeEscalation: true,
	}

	create, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document:        testDocument(uuid.New()),
		Profile:         profile,
		Identity:        testIdentity(),
		DockerSocketGID: testDockerGID,
	})
	require.NoError(t, err)

	assert.Equal(t, "0:0", create.User)
	assert.Equal(t, "999", create.Env["PEEN_WORKER_SUPPLEMENTARY_GIDS"])
	assert.Contains(t, create.Groups, "999")
}

// A native profile has no entrypoint to grant sudo, so promising escalation
// there is refused instead of silently ignored.
func TestNativeProfileCannotGrantPrivilegeEscalation(t *testing.T) {
	t.Parallel()

	profile := worker.Profile{
		Name:                     worker.ProfileNative,
		Kind:                     worker.KindNative,
		Revision:                 1,
		AllowPrivilegeEscalation: true,
	}

	require.ErrorIs(t, profile.Validate(), commerr.ErrValidationFailed)
}

// A host-like profile with no discovered socket group refuses rather than
// mounting a socket the worker account cannot use.
func TestBuildCreateRequestRefusesAnUnusableSocketGrant(t *testing.T) {
	t.Parallel()

	profile := worker.Profile{
		Name:              worker.ProfileDockerHostLike,
		Kind:              worker.KindDocker,
		Revision:          1,
		Image:             testImage,
		AllowDockerSocket: true,
	}

	_, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document: testDocument(uuid.New()),
		Profile:  profile,
		Identity: testIdentity(),
	})

	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

// A native profile is never launched as a container.
func TestBuildCreateRequestRefusesANativeProfile(t *testing.T) {
	t.Parallel()

	_, err := docker.BuildCreateRequest(docker.LaunchSpec{
		Document: testDocument(uuid.New()),
		Profile: worker.Profile{
			Name: worker.ProfileNative,
			Kind: worker.KindNative,
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

	// repoDigests is what the daemon reports for the worker image. Nil means
	// an image the daemon knows only locally, which the launcher refuses
	// rather than recording a local ID as a digest.
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
	d.created = request

	return testContainerID, nil
}

func (d *fakeDaemon) AttachStdin(
	_ context.Context,
	_ string,
	document []byte,
) error {
	d.stdin = document

	return nil
}

func (d *fakeDaemon) StartContainer(_ context.Context, id string) error {
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
	d.removed = append(d.removed, id)

	return nil
}

func (d *fakeDaemon) InspectContainer(
	_ context.Context,
	id string,
) (docker.ContainerState, error) {
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
		Document: testDocument(sessionID),
		Profile:  sandboxProfile(),
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
		Document: testDocument(sessionID),
		Profile:  sandboxProfile(),
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
		Document: testDocument(sessionID),
		Profile:  sandboxProfile(),
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
		Document: testDocument(sessionID),
		Profile:  sandboxProfile(),
	})
	require.NoError(t, err)

	assert.Equal(t, []string{testImage}, daemon.pulled)
	assert.Equal(t, testImage, process.Describe().ImageDigest)
}

// A pull that fails fails the launch, rather than leaving a container to be
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
		Document: testDocument(sessionID),
		Profile:  sandboxProfile(),
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
		Client:   daemon,
		Identity: testIdentity(),
		Image:    "peen-dev:local",
	})
	require.NoError(t, err)

	process, err := launcher.Launch(t.Context(), worker.LaunchRequest{
		Document: testDocument(sessionID),
		Profile:  sandboxProfile(),
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
		Document: testDocument(sessionID),
		Profile:  sandboxProfile(),
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
		Client:   daemon,
		Identity: testIdentity(),
		Image:    worker.ImageRepository + ":v1.2.3",
	})
	require.NoError(t, err)

	process, err := launcher.Launch(t.Context(), worker.LaunchRequest{
		Document: testDocument(sessionID),
		Profile:  sandboxProfile(),
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
		Client:   daemon,
		Identity: testIdentity(),
		Image:    "someoneelse/peen:v1.2.3",
	})
	require.NoError(t, err)

	_, err = launcher.Launch(t.Context(), worker.LaunchRequest{
		Document: testDocument(sessionID),
		Profile:  sandboxProfile(),
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

	profile := sandboxProfile()
	profile.Image = ""

	_, err = launcher.Launch(t.Context(), worker.LaunchRequest{
		Document: testDocument(sessionID),
		Profile:  profile,
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
		Document: testDocument(sessionID),
		Profile:  sandboxProfile(),
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
		Document: testDocument(sessionID),
		Profile:  sandboxProfile(),
	})
	require.NoError(t, err)

	require.NoError(t, process.Stop(t.Context()))

	assert.Equal(t, []string{testContainerID}, daemon.stopped)
	assert.Equal(t, []string{testContainerID}, daemon.removed)
}

// A launcher without a daemon client cannot exist, which is what turns absent
// Docker authority into a startup decision rather than a per-launch surprise.
func TestNewRefusesWithoutADaemonClient(t *testing.T) {
	t.Parallel()

	_, err := docker.New(docker.Options{Identity: testIdentity()})

	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
}
