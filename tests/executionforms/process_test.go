//go:build integration

package executionforms

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/require"
)

type runningPeen struct {
	command    *exec.Cmd
	done       chan error
	output     lockedBuffer
	baseURL    string
	metricsURL string
	stopped    bool
}

// lockedBuffer lets a test read the process output while the process still
// writes it.
type lockedBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	return b.buffer.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	return b.buffer.String()
}

type processConfig struct {
	binary          string
	configDirectory string
	stateDirectory  string
	workspace       string
	workerSocketDir string
	apiAddress      string
	metricsAddress  string
	provider        *testinfra.ProviderMock
	apiToken        string

	// unsetAgent leaves PEEN_AGENT out of the environment so the process
	// uses its built-in default.
	unsetAgent bool
	// environment holds extra PEEN_ settings. They come last, so they win
	// over the defaults peenEnvironment sets.
	environment []string
}

type processState struct {
	configDirectory string
	stateDirectory  string
	workspace       string
	workerSocketDir string
}

// shortWorkerSocketDirectory gives the worker sockets a root well inside the
// 107-byte Unix socket path limit.
//
// t.TempDir() embeds the test and subtest names, which here is long enough on
// its own that the default PEEN_CONFIG_DIR/workers path plus a session UUID
// would not fit. That is a property of this test's directory naming, not of a
// real deployment, so the test names the root the way an operator with a deep
// configuration directory would.
func shortWorkerSocketDirectory(t *testing.T) string {
	t.Helper()

	directory, err := os.MkdirTemp("", executionFormsSocketDirPrefix)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(directory)) })

	return directory
}

func newProcessState(t *testing.T) processState {
	t.Helper()

	root := t.TempDir()
	configDirectory := filepath.Join(root, executionFormsConfigDirectory)
	workspace := filepath.Join(root, executionFormsWorkspace)
	writeFile(
		t,
		filepath.Join(configDirectory, executionFormsAgentsDirectory, executionFormsAgentFile),
		executionFormsAgentDocument,
	)
	writeFile(
		t,
		filepath.Join(workspace, executionFormsRulesFile),
		executionFormsInitialRules,
	)
	writeFile(
		t,
		filepath.Join(workspace, executionFormsJSONFixtureFile),
		executionFormsJSONBefore,
	)
	writeFile(
		t,
		filepath.Join(workspace, executionFormsPatchFixtureFile),
		executionFormsPatchBefore,
	)

	return processState{
		configDirectory: configDirectory,
		// The state directory is a sibling of the configuration directory, not
		// a child, because the controller refuses durable state inside the
		// directory it mounts into workers.
		stateDirectory:  filepath.Join(root, executionFormsStateDirectory),
		workspace:       workspace,
		workerSocketDir: shortWorkerSocketDirectory(t),
	}
}

func startPeen(t *testing.T, config processConfig) *runningPeen {
	t.Helper()

	process := launchPeen(t, config)
	awaitReady(t, process)

	return process
}

// launchPeen starts the process without waiting for it to become ready, so
// a test can also watch it refuse to start.
func launchPeen(t *testing.T, config processConfig) *runningPeen {
	t.Helper()

	upstreams, err := json.Marshal([]map[string]string{{
		executionFormsUpstreamNameKey: executionFormsUpstreamName,
		executionFormsUpstreamTypeKey: executionFormsUpstreamProvider,
		executionFormsUpstreamURLKey:  config.provider.BaseURL(),
	}})
	require.NoError(t, err)

	process := &runningPeen{
		baseURL:    "http://" + config.apiAddress,
		metricsURL: "http://" + config.metricsAddress,
		done:       make(chan error, 1),
	}
	// The context never cancels, so the cleanup that stops Peen can still ask it
	// to shut down gracefully instead of finding it already killed.
	//nolint:gosec // The binary is the one this suite built or installed.
	process.command = exec.CommandContext(
		context.WithoutCancel(t.Context()),
		config.binary,
		executionFormsRunCommand,
	)
	process.command.Dir = config.workspace
	process.command.Env = peenEnvironment(config, string(upstreams))
	process.command.Stdout = &process.output
	process.command.Stderr = &process.output

	require.NoError(t, process.command.Start())
	go func() {
		process.done <- process.command.Wait()
	}()

	// A failure here is usually a controller-side or worker-side error that
	// only the process log names. Without it the test reports the HTTP
	// envelope, which is deliberately generic.
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("peen process output:\n%s", process.output.String())
		}
	})

	return process
}

func peenEnvironment(config processConfig, upstreams string) []string {
	environment := make([]string, 0, len(os.Environ())+8)
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "PEEN_") {
			continue
		}

		environment = append(environment, value)
	}

	environment = append(environment,
		"PEEN_CONFIG_DIR="+config.configDirectory,
		"PEEN_STATE_DIR="+config.stateDirectory,
		"PEEN_WORKER_SOCKET_DIR="+config.workerSocketDir,
		"PEEN_HTTP_LISTEN_ADDRESS="+config.apiAddress,
		"PEEN_METRICS_LISTEN_ADDRESS="+config.metricsAddress,
		"PEEN_UPSTREAMS="+upstreams,
		"PEEN_DEFAULT_MODEL="+config.provider.DefaultModel(),
		"PEEN_API_TOKEN="+config.apiToken,
	)

	if !config.unsetAgent {
		environment = append(
			environment,
			executionFormsAgentEnvironmentPrefix+executionFormsAgentName,
		)
	}

	return append(environment, config.environment...)
}

func (p *runningPeen) stop(t *testing.T) {
	t.Helper()

	if p == nil || p.stopped {
		return
	}
	p.stopped = true

	if p.command.Process != nil {
		require.NoError(t, p.command.Process.Signal(os.Interrupt))
	}

	select {
	case err := <-p.done:
		require.NoError(t, err, p.output.String())
	case <-time.After(executionFormsShutdownTimeout):
		require.NoError(t, p.command.Process.Kill())
		require.Failf(t, "Peen process did not stop", "%s", p.output.String())
	}
}

func awaitReady(t *testing.T, process *runningPeen) {
	t.Helper()

	deadline := time.Now().Add(executionFormsStartupTimeout)
	client := &http.Client{Timeout: executionFormsPollInterval}
	for time.Now().Before(deadline) {
		status, _, err := fetchURL(
			t.Context(),
			client,
			process.baseURL+executionFormsReadyPath,
		)
		if err == nil && status == http.StatusOK {
			return
		}

		select {
		case processErr := <-process.done:
			require.NoError(t, processErr, process.output.String())
			require.Failf(t, "Peen stopped before ready", "%s", process.output.String())
		case <-time.After(executionFormsPollInterval):
		}
	}

	require.Failf(t, "Peen did not become ready", "%s", process.output.String())
}

func reserveLoopbackAddress(t *testing.T) string {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(
		t.Context(),
		executionFormsNetworkTCP,
		executionFormsLoopbackAddress,
	)
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())

	return address
}
