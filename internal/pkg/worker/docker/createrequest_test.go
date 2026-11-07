package docker_test

import (
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
		"PEEN_METRICS_RETENTION",
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

// An auto-evolving worker gets the global learned directory after the config
// mount, read-only unless the operator made it writable, so its own mode wins
// when it sits inside the config directory. PEEN_WRITABLE_CONFIG_DIR makes the
// config mount writable.
func TestBuildCreateRequestMountsTheLearnedAndConfigDirectories(t *testing.T) {
	t.Parallel()

	learned := filepath.Join(testConfigDir, ".agents", "learned")

	testCases := []struct {
		name            string
		learned         string
		writableLearned bool
		writableConfig  bool
	}{
		{name: "auto-evolve off"},
		{name: "auto-evolve on", learned: learned},
		{
			name:            "writable learned directory",
			learned:         learned,
			writableLearned: true,
		},
		{name: "writable config", writableConfig: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			document := testDocument(uuid.New())
			document.LearnedDirectory = tc.learned
			document.WritableLearnedDirectory = tc.writableLearned
			document.WritableConfigDirectory = tc.writableConfig

			create, err := docker.BuildCreateRequest(docker.LaunchSpec{
				Document:   document,
				AgentSpace: sandboxSpace(),
				Identity:   testIdentity(),
			})
			require.NoError(t, err)

			configIndex := slices.IndexFunc(
				create.Mounts,
				func(mount worker.Mount) bool {
					return mount.Target == testConfigDir
				},
			)
			require.GreaterOrEqual(t, configIndex, 0)
			assert.Equal(
				t,
				!tc.writableConfig,
				create.Mounts[configIndex].ReadOnly,
			)

			learnedIndex := slices.IndexFunc(
				create.Mounts,
				func(mount worker.Mount) bool {
					return mount.Target == learned
				},
			)

			if tc.learned == "" {
				assert.Equal(t, -1, learnedIndex)

				return
			}

			require.Greater(t, learnedIndex, configIndex)
			assert.Equal(t, learned, create.Mounts[learnedIndex].Source)
			assert.Equal(
				t,
				!tc.writableLearned,
				create.Mounts[learnedIndex].ReadOnly,
			)
		})
	}
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
