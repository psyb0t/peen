//go:build real

package realtest

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type runningRealHarnessPeen struct {
	command  *exec.Cmd
	done     chan error
	output   synchronizedBuffer
	baseURL  string
	apiToken string
	stopped  bool
}

type synchronizedBuffer struct {
	mutex sync.Mutex
	value bytes.Buffer
}

func (b *synchronizedBuffer) Write(content []byte) (int, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	return b.value.Write(content)
}

func (b *synchronizedBuffer) String() string {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	return b.value.String()
}

func buildRealHarnessBinary(t *testing.T) string {
	t.Helper()

	repository, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)

	binary := filepath.Join(t.TempDir(), realHarnessBinaryName)
	command := exec.CommandContext(
		t.Context(),
		"go",
		"build",
		"-o",
		binary,
		realHarnessCommandPackage,
	)
	command.Dir = repository
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))

	return binary
}

func startRealHarnessPeen(
	t *testing.T,
	binary string,
	fixture realHarnessFixture,
	upstreams string,
	defaultModel string,
) *runningRealHarnessPeen {
	t.Helper()

	return startRealHarnessPeenWith(
		t,
		binary,
		fixture,
		upstreams,
		defaultModel,
		nil,
	)
}

// startRealHarnessPeenWith starts a controller with extra configuration.
//
// The overrides are applied after the fixture's own settings, so a test that
// needs another agent space setting or a smaller context budget can ask for one
// without every other test inheriting it.
func startRealHarnessPeenWith(
	t *testing.T,
	binary string,
	fixture realHarnessFixture,
	upstreams string,
	defaultModel string,
	overrides map[string]string,
) *runningRealHarnessPeen {
	t.Helper()

	apiAddress := reserveRealHarnessAddress(t)
	metricsAddress := reserveRealHarnessAddress(t)
	apiToken := uuid.NewString()
	process := &runningRealHarnessPeen{
		baseURL:  "http://" + apiAddress,
		apiToken: apiToken,
		done:     make(chan error, 1),
	}
	// The context never cancels, so the cleanup that stops Peen can still ask it
	// to shut down gracefully instead of finding it already killed.
	process.command = exec.CommandContext(
		context.WithoutCancel(t.Context()),
		binary,
		realHarnessRunCommand,
	)
	process.command.Dir = fixture.workspace
	process.command.Env = realHarnessEnvironment(
		fixture,
		apiAddress,
		metricsAddress,
		apiToken,
		upstreams,
		defaultModel,
		overrides,
	)
	process.command.Stdout = &process.output
	process.command.Stderr = &process.output
	require.NoError(t, process.command.Start())

	go func() {
		process.done <- process.command.Wait()
	}()

	awaitRealHarnessReady(t, process)

	return process
}

func realHarnessEnvironment(
	fixture realHarnessFixture,
	apiAddress, metricsAddress, apiToken, upstreams, defaultModel string,
	overrides map[string]string,
) []string {
	environment := make(map[string]string, len(os.Environ())+16)
	for _, value := range os.Environ() {
		name, _, found := strings.Cut(value, "=")
		if found {
			environment[name] = value
		}
	}

	// Every directory and worker setting is pinned to the fixture rather than
	// inherited. The deployment .env this suite reads providers from also
	// carries the operator's own roots, Docker profiles, image, and Docker
	// socket, and any one of them reaching the spawned controller would test
	// their deployment instead of this fixture.
	//
	// The Docker socket names a path that holds no socket. A controller that
	// can reach a socket refuses the control agent space, and this suite runs
	// its workers in the control agent space whatever the machine has.
	for name, value := range map[string]string{
		"PEEN_CONFIG_DIR":             fixture.configDirectory,
		"PEEN_STATE_DIR":              fixture.stateDirectory,
		"PEEN_WORKER_SOCKET_DIR":      fixture.socketDirectory,
		"PEEN_WORKSPACE_ROOT":         fixture.workspaceRoot,
		"PEEN_DOCKER_PROFILES":        "",
		"PEEN_DEFAULT_AGENT_SPACE":    "",
		"PEEN_WORKER_IMAGE":           "",
		"PEEN_DOCKER_SOCKET":          filepath.Join(fixture.configDirectory, "no-docker.sock"),
		"PEEN_HOST_USERNAME":          "",
		"PEEN_HOST_HOME":              "",
		"PEEN_AGENT":                  realHarnessRootAgent,
		"PEEN_HTTP_LISTEN_ADDRESS":    apiAddress,
		"PEEN_METRICS_LISTEN_ADDRESS": metricsAddress,
		"PEEN_UPSTREAMS":              upstreams,
		"PEEN_DEFAULT_MODEL":          defaultModel,
		"PEEN_API_TOKEN":              apiToken,
		"PEEN_MODEL_REQUEST_TIMEOUT":  realHarnessTestTimeout.String(),
		"PEEN_LOG_DIRECTORY":          fixture.auditDirectory,
		"PEEN_LOG_RETENTION":          "336h",
		"LOG_LEVEL":                   "debug",
	} {
		environment[name] = name + "=" + value
	}

	// The deployment .env may shrink the context or the output limit for its
	// own use. This suite tests the harness with each model's full window and
	// output limit, so those settings are dropped. A test that tests a small
	// context sets them again through overrides.
	for _, name := range realHarnessBudgetSettings() {
		delete(environment, name)
	}

	for name, value := range overrides {
		environment[name] = name + "=" + value
	}

	keys := make([]string, 0, len(environment))
	for name := range environment {
		keys = append(keys, name)
	}
	sort.Strings(keys)

	values := make([]string, 0, len(keys))
	for _, name := range keys {
		values = append(values, environment[name])
	}

	return values
}

func (p *runningRealHarnessPeen) stop(t *testing.T) {
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
	case <-time.After(realHarnessShutdownTimeout):
		require.NoError(t, p.command.Process.Kill())
		require.Failf(t, "Peen process did not stop", "%s", p.output.String())
	}
}

// realHarnessReady reports whether one GET of the ready endpoint answers 200.
func realHarnessReady(t *testing.T, client *http.Client, target string) bool {
	t.Helper()

	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		target,
		nil,
	)
	require.NoError(t, err)

	response, err := client.Do(request)
	if err != nil {
		return false
	}

	closeErr := response.Body.Close()

	return response.StatusCode == http.StatusOK && closeErr == nil
}

func awaitRealHarnessReady(t *testing.T, process *runningRealHarnessPeen) {
	t.Helper()

	deadline := time.Now().Add(realHarnessStartupTimeout)
	client := &http.Client{Timeout: realHarnessPollInterval}
	for time.Now().Before(deadline) {
		if realHarnessReady(t, client, process.baseURL+realHarnessReadyPath) {
			return
		}

		select {
		case processErr := <-process.done:
			require.NoError(t, processErr, process.output.String())
			require.Fail(t, "Peen stopped before ready")
		case <-time.After(realHarnessPollInterval):
		}
	}

	require.Failf(t, "Peen did not become ready", "%s", process.output.String())
}

func reserveRealHarnessAddress(t *testing.T) string {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(
		t.Context(),
		realHarnessNetwork,
		realHarnessLoopback,
	)
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())

	return address
}
