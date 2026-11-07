package docker_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/psyb0t/peen/internal/pkg/worker/docker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

// A launcher without a daemon client cannot exist, which is what turns absent
// Docker authority into a startup decision rather than a per-launch surprise.
func TestNewRefusesWithoutADaemonClient(t *testing.T) {
	t.Parallel()

	_, err := docker.New(docker.Options{Identity: testIdentity()})

	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
}
