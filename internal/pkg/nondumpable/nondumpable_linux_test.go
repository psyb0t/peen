//go:build linux

package nondumpable

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	helperProcessEnvKey   = "PEEN_NONDUMPABLE_HELPER_PROCESS"
	helperProcessEnvValue = "1"
	helperProcessTestName = "TestHelperNonDumpableProcess"
	helperReadyLine       = "non-dumpable helper ready"

	helperSecretEnvKey = "PEEN_NONDUMPABLE_TEST_SECRET" //nolint:gosec // Name only.
	helperSecretValue  = "nondumpable-test-secret-value"

	procEnvironPathFormat = "/proc/%d/environ"
)

// TestHelperNonDumpableProcess is not a test on its own. The test below runs
// the test binary again with only this function selected, so the helper is a
// separate process with the same user ID, as a control agent's command is.
func TestHelperNonDumpableProcess(t *testing.T) {
	if os.Getenv(helperProcessEnvKey) != helperProcessEnvValue {
		return
	}

	require.NoError(t, Enable(t.Context()))

	_, err := fmt.Fprintln(os.Stdout, helperReadyLine)
	require.NoError(t, err)

	// Stay alive until the parent closes stdin, so the parent can try to read
	// this process's /proc files while it runs.
	_, err = io.Copy(io.Discard, os.Stdin)
	require.NoError(t, err)
}

func TestEnableHidesEnvironmentFromSameUser(t *testing.T) {
	t.Parallel()

	// The helper is stopped by closing its stdin in the cleanup, which runs
	// after t.Context is cancelled, so the context must not kill it first.
	//nolint:gosec // Re-runs this test binary with a fixed test selection.
	command := exec.CommandContext(
		context.WithoutCancel(t.Context()),
		os.Args[0],
		"-test.run=^"+helperProcessTestName+"$",
	)
	command.Env = append(
		os.Environ(),
		helperProcessEnvKey+"="+helperProcessEnvValue,
		helperSecretEnvKey+"="+helperSecretValue,
	)

	stdin, err := command.StdinPipe()
	require.NoError(t, err)

	stdout, err := command.StdoutPipe()
	require.NoError(t, err)

	require.NoError(t, command.Start())
	t.Cleanup(func() {
		assert.NoError(t, stdin.Close())
		assert.NoError(t, command.Wait())
	})

	waitForHelperReady(t, stdout)

	environ, err := os.ReadFile(
		fmt.Sprintf(procEnvironPathFormat, command.Process.Pid),
	)
	require.ErrorIs(t, err, os.ErrPermission)
	assert.NotContains(t, string(environ), helperSecretValue)
}

func waitForHelperReady(t *testing.T, stdout io.Reader) {
	t.Helper()

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		if scanner.Text() == helperReadyLine {
			return
		}
	}

	require.NoError(t, scanner.Err())
	require.FailNow(t, "the helper process exited before it was ready")
}
