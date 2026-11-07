//go:build integration

package executionforms

import (
	"net/http"
	"os/exec"
	"testing"
	"time"

	peenconfig "github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dockerDaemonReachable asks the production rule whether a Docker daemon
// answers for a controller started by this suite. The controller inherits this
// process's environment without its PEEN_ settings, so the probe reads the same
// candidates it does: a unix DOCKER_HOST, the default socket, and a tcp
// DOCKER_HOST.
func dockerDaemonReachable() bool {
	return peenconfig.Config{}.HasDockerAuthority()
}

// requireNoDockerDaemon skips a test that needs the control agent space. A
// controller that reaches a Docker daemon refuses control, whatever the
// configuration says, so these tests run where none answers: make
// test-execution-forms. TestExecutionFormsRefuseControlBesideADockerDaemon
// covers the other case.
func requireNoDockerDaemon(t *testing.T) {
	t.Helper()

	if dockerDaemonReachable() {
		t.Skip("a Docker daemon answers here, so the controller refuses the " +
			"control agent space; this test runs under make test-execution-forms")
	}
}

// With a Docker daemon reachable, the controller must not offer control: a
// control worker would hold the daemon and be host root. This is the contract
// the tests that need control cannot check, so it runs only where a daemon
// answers.
func TestExecutionFormsRefuseControlBesideADockerDaemon(t *testing.T) {
	if !dockerDaemonReachable() {
		t.Skip("no Docker daemon answers here; the control agent space tests " +
			"cover this environment")
	}

	binary := buildInstalledForm(t, executionFormsRepositoryRoot(t))
	provider := testinfra.NewProviderMock()
	t.Cleanup(provider.Close)
	state := newProcessState(t)

	process := launchPeen(t, processConfig{
		binary:          binary,
		configDirectory: state.configDirectory,
		stateDirectory:  state.stateDirectory,
		workspace:       state.workspace,
		workerSocketDir: state.workerSocketDir,
		apiAddress:      reserveLoopbackAddress(t),
		metricsAddress:  reserveLoopbackAddress(t),
		provider:        provider,
		apiToken:        executionFormsAPIToken,
		environment:     []string{executionFormsControlDefaultSetting},
	})

	output := awaitRefusal(t, process)
	assert.Contains(t, output, executionFormsHostRootMessage)
	assert.Contains(t, output, executionFormsDefaultSpaceSettingName)
}

func TestExecutionFormsRefuseToStartOnInvalidSelection(t *testing.T) {
	binary := buildSourceForm(t, executionFormsRepositoryRoot(t))

	testCases := []struct {
		name         string
		environment  []string
		wantMessages []string
	}{
		{
			name:         "log retention that is not whole days",
			environment:  []string{"PEEN_LOG_RETENTION=36h"},
			wantMessages: []string{executionFormsRetentionNotDayMessage},
		},
		{
			name:        "unknown disabled core skill",
			environment: []string{"PEEN_DISABLED_CORE_SKILLS=no-such-skill"},
			wantMessages: []string{
				executionFormsInvalidDisabledSkillsMessage,
				executionFormsValidCoreSkillsMessage,
			},
		},
		{
			name:        "unknown disabled core agent",
			environment: []string{"PEEN_DISABLED_CORE_AGENTS=no-such-agent"},
			wantMessages: []string{
				executionFormsInvalidDisabledAgentsMessage,
				executionFormsValidCoreAgentsMessage,
			},
		},
		{
			name: "root agent is a disabled core agent",
			environment: []string{
				"PEEN_AGENT=" + executionFormsCoreGeneralPurpose,
				"PEEN_DISABLED_CORE_AGENTS=general-purpose",
			},
			wantMessages: []string{
				executionFormsInvalidAgentMessage,
				executionFormsDisabledCoreAgentMessage,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			provider := testinfra.NewProviderMock()
			t.Cleanup(provider.Close)
			state := newProcessState(t)

			process := launchPeen(t, processConfig{
				binary:          binary,
				configDirectory: state.configDirectory,
				stateDirectory:  state.stateDirectory,
				workspace:       state.workspace,
				workerSocketDir: state.workerSocketDir,
				apiAddress:      reserveLoopbackAddress(t),
				metricsAddress:  reserveLoopbackAddress(t),
				provider:        provider,
				apiToken:        executionFormsAPIToken,
				environment:     tc.environment,
			})

			output := awaitRefusal(t, process)
			for _, message := range tc.wantMessages {
				assert.Contains(t, output, message)
			}
		})
	}
}

// awaitRefusal waits for a process that was given an invalid selection to exit
// on its own within executionFormsRefusalTimeout, requires the non-zero exit
// status, and returns its output. A process that hangs fails the test; nothing
// interrupts it. While it runs, /ready must never answer 200.
func awaitRefusal(
	t *testing.T,
	process *runningPeen,
) string {
	t.Helper()

	t.Cleanup(func() {
		if !process.stopped {
			_ = process.command.Process.Kill()
		}
	})

	timeout := time.After(executionFormsRefusalTimeout)
	for {
		assertNotReady(t, process)

		select {
		case waitErr := <-process.done:
			process.stopped = true
			assertRefusalExit(t, waitErr)

			return process.output.String()
		case <-timeout:
			require.Fail(
				t,
				"Peen did not exit on its own after refusing to start",
				"%s",
				process.output.String(),
			)
		case <-time.After(executionFormsPollInterval):
		}
	}
}

func assertRefusalExit(t *testing.T, waitErr error) {
	t.Helper()

	var exitErr *exec.ExitError
	require.ErrorAs(t, waitErr, &exitErr)
	assert.Equal(t, executionFormsRefusalExitCode, exitErr.ExitCode())
}

func assertNotReady(t *testing.T, process *runningPeen) {
	t.Helper()

	client := &http.Client{Timeout: executionFormsPollInterval}
	status, _, err := fetchURL(
		t.Context(),
		client,
		process.baseURL+executionFormsReadyPath,
	)
	if err != nil {
		return
	}

	assert.NotEqual(t, http.StatusOK, status)
}
