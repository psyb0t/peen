package docker_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	dockerfileName      = "Dockerfile"
	entrypointRelative  = "docker/entrypoint.sh"
	entrypointInstalled = "/usr/local/bin/peen-entrypoint"
	repositoryModule    = "go.mod"
)

// The launch builder promises the image behaves a certain way, and nothing in
// Go can check that on its own. These tests read the image definition and run
// the entrypoint directly, so a Dockerfile edit that quietly breaks the
// promise fails here rather than in a deployment. No daemon is involved.

// A worker container starting as root is only safe because the image ships the
// entrypoint that drops back down, plus the tools that bootstrap needs.
func TestImageShipsThePrivilegeBootstrap(t *testing.T) {
	t.Parallel()

	dockerfile := readRepositoryFile(t, dockerfileName)

	assert.Contains(
		t,
		dockerfile,
		"COPY --chown=root:root --chmod=0755 "+entrypointRelative+" "+
			entrypointInstalled,
		"the image must install the entrypoint the launcher relies on",
	)
	assert.Contains(
		t,
		dockerfile,
		`ENTRYPOINT ["/usr/bin/tini", "--", "`+entrypointInstalled+`", "./app"]`,
		"every container must route through the entrypoint",
	)

	for _, tool := range []string{"sudo", "util-linux"} {
		assert.Contains(
			t,
			dockerfile,
			tool,
			"the bootstrap needs %q installed", tool,
		)
	}
}

// The image default is the control plane and the worker command stays internal.
// A default of `worker` would make `docker run psyb0t/peen` a broken worker
// instead of a controller.
func TestImageDefaultsToControlMode(t *testing.T) {
	t.Parallel()

	dockerfile := readRepositoryFile(t, dockerfileName)

	assert.Contains(t, dockerfile, `CMD ["run"]`)
	assert.NotContains(t, dockerfile, `CMD ["worker"]`)
	assert.Contains(t, dockerfile, "USER appuser")
}

// The image authorizes nobody by default. sudo is present but inert until a
// privilege-escalation worker writes the rule for its own account, so building
// the image never bakes in an escalation path.
func TestImageShipsNoSudoersRule(t *testing.T) {
	t.Parallel()

	// Comments discuss sudoers on purpose. Only build instructions can create
	// one, so the assertion reads those and ignores the prose.
	instructions := dockerfileInstructions(t)

	assert.NotContains(t, instructions, "sudoers")
	assert.NotContains(t, instructions, "NOPASSWD")
}

func dockerfileInstructions(t *testing.T) string {
	t.Helper()

	lines := strings.Split(readRepositoryFile(t, dockerfileName), "\n")
	instructions := make([]string, 0, len(lines))

	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}

		instructions = append(instructions, line)
	}

	return strings.Join(instructions, "\n")
}

// A non-root container is the control plane. The entrypoint must exec straight
// through and touch nothing. Docker workers explicitly begin as root so they
// can reconcile the host identity before the agent begins.
func TestEntrypointPassesThroughWhenNotRoot(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("this test asserts the non-root branch")
	}

	marker := filepath.Join(t.TempDir(), "executed")

	// The arguments are this repository's own entrypoint and a temporary path,
	// so there is no caller-supplied input to taint them.
	//nolint:gosec // Fixed script path plus a test-owned temporary file.
	command := exec.CommandContext(
		t.Context(),
		"bash",
		repositoryPath(t, entrypointRelative),
		"touch",
		marker,
	)
	// A privileged profile's variables are set here on purpose: a non-root
	// container must ignore them rather than act on them.
	command.Env = append(
		os.Environ(),
		"PEEN_WORKER_ALLOW_SUDO=true",
		"PEEN_WORKER_UID=1000",
		"PEEN_WORKER_GID=1000",
		"PEEN_WORKER_USERNAME=peen",
		"PEEN_WORKER_HOME=/home/peen",
	)

	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	assert.FileExists(t, marker, "the entrypoint must exec its command")
}

// Started as root with no identity to drop to, the entrypoint refuses instead
// of running the agent as root.
func TestEntrypointRefusesRootWithoutAnIdentity(t *testing.T) {
	t.Parallel()

	script := readRepositoryFile(t, entrypointRelative)

	assert.Contains(
		t,
		script,
		"started as root without a worker identity to drop to",
	)
	assert.Contains(t, script, "refusing to run a worker as root")
	assert.Contains(t, script, "exec setpriv")
}

// The sudoers rule is written only behind the allow-sudo flag the launcher sets
// for a privilege-escalation profile, and only for the worker's own account.
func TestEntrypointGrantsSudoOnlyWhenTheProfileAllowsIt(t *testing.T) {
	t.Parallel()

	script := readRepositoryFile(t, entrypointRelative)

	assert.Contains(t, script, `"${PEEN_WORKER_ALLOW_SUDO:-}" == "true"`)
	assert.Contains(t, script, "grant_sudo")
	assert.Contains(
		t,
		script,
		`printf '%s ALL=(ALL) NOPASSWD:ALL\n' "$username"`,
	)
}

func readRepositoryFile(t *testing.T, relative string) string {
	t.Helper()

	content, err := os.ReadFile(repositoryPath(t, relative))
	require.NoError(t, err)

	return string(content)
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

// entrypointHasStrictMode guards the shell contract the bootstrap depends on: a
// failed account creation must stop the launch instead of dropping privileges
// into a half-built environment.
func TestEntrypointUsesStrictMode(t *testing.T) {
	t.Parallel()

	script := readRepositoryFile(t, entrypointRelative)

	assert.True(
		t,
		strings.Contains(script, "set -euo pipefail"),
		"the entrypoint must fail fast",
	)
}
