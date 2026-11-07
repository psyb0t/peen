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
// Containment rules this suite holds itself to:
//
//   - One disposable root per run, under the repository's gitignored .testing/
//     directory. The DIND Make target binds the repository at the identical
//     host path, so a bind source inside it resolves to the same bytes on both
//     sides. A path under /tmp would be the test container's /tmp, which the
//     host daemon would resolve to some other directory or create empty, and
//     every filesystem assertion below would be meaningless.
//   - Exactly one container per test, named for a session UUID that test
//     generated.
//   - The readonly test starts one proxy of its own, inside its own session
//     socket directory, and closes it before its disposable root is removed.
//   - Teardown inspects that exact container ID and acts only when
//     worker.OwnedBy still reports it as this session's worker. No name
//     pattern, no label selector, no prune.
//   - It removes only the disposable root it created.
package dockerworker_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/psyb0t/peen/internal/pkg/worker/docker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

	//nolint:gosec // The path is the test operator's own Docker socket setting.
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
