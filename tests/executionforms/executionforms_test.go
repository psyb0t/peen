//go:build integration

package executionforms

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	peenconfig "github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	executionFormsAPIMessagesPath    = "/v1/messages"
	executionFormsAPIOpenSessionPath = "/v1/sessions/open"
	executionFormsContentTypeJSON    = "application/json"
	executionFormsHeaderContentType  = "Content-Type"
	executionFormsWebSocketPath      = "/v1/ws"
	executionFormsReadyPath          = "/ready"
	executionFormsMetricsPath        = "/metrics"

	executionFormsHeaderAuthorization = "Authorization"
	executionFormsHeaderSessionID     = "X-Session-ID"

	executionFormsBearerPrefix = "Bearer "
	executionFormsAgentName    = "default"
	executionFormsAPIToken     = "EXAMPLE-DO-NOT-USE"

	// The refusal the socket rule prints when control is the default beside a
	// reachable Docker daemon.
	executionFormsDefaultSpaceSettingName = "PEEN_DEFAULT_AGENT_SPACE"
	executionFormsControlDefaultSetting   = "PEEN_DEFAULT_AGENT_SPACE=control"
	executionFormsHostRootMessage         = "host root"

	executionFormsDirectoryMode   = 0o700
	executionFormsFileMode        = 0o600
	executionFormsStartupTimeout  = 30 * time.Second
	executionFormsShutdownTimeout = 15 * time.Second
	executionFormsPollInterval    = 10 * time.Millisecond

	// executionFormsTurnTimeout bounds one complete turn, which is a longer
	// wait than startup: the controller launches a worker, the worker runs the
	// model, and only then does the completion event arrive. Reusing the
	// startup budget here made the suite flake under `make test-coverage`,
	// where the instrumented binary is slow enough that the read deadline
	// expired mid-turn. The client then closed the connection, cancelling the
	// controller's own runTurn call, which surfaced as a worker failure
	// rather than the client timeout it actually was.
	executionFormsTurnTimeout = 3 * time.Minute

	executionFormsAgentDocument = "---\nname: default\ndescription: execution form test agent\n---\nFollow the test instructions."
	executionFormsInitialRules  = "Initial workspace rule."
	executionFormsUpdatedRules  = "Updated workspace rule."

	executionFormsJSONFixtureFile  = "json-edit.txt"
	executionFormsPatchFixtureFile = "patch-edit.txt"
	executionFormsJSONCommandFile  = "json-command.txt"
	executionFormsPatchCommandFile = "patch-command.txt"

	executionFormsJSONBefore  = "before\n"
	executionFormsJSONAfter   = "after\n"
	executionFormsPatchBefore = "alpha\n"
	executionFormsPatchAfter  = "bravo\n"

	executionFormsJSONCommand  = "printf 'json command\\n' > json-command.txt"
	executionFormsPatchCommand = "printf 'patch command\\n' > patch-command.txt"
	executionFormsJSONOutput   = "json command\n"
	executionFormsPatchOutput  = "patch command\n"

	executionFormsJSONAnswer  = "json tool turn complete"
	executionFormsPatchAnswer = "patch tool turn complete"

	executionFormsSourceBinaryName    = "peen-source"
	executionFormsInstalledBinaryName = "cmd"
	executionFormsSourceForm          = "source build"
	executionFormsInstalledForm       = "go install"

	executionFormsRepositoryModuleFile = "go.mod"
	executionFormsCommandPackage       = "./cmd"
	executionFormsRunCommand           = "run"

	executionFormsConfigDirectory = "config"
	executionFormsStateDirectory  = "state"
	executionFormsSocketDirPrefix = "pw"
	executionFormsWorkspace       = "workspace"
	executionFormsAgentsDirectory = ".agents/agents"
	executionFormsAgentFile       = "default.md"
	executionFormsRulesFile       = "AGENTS.md"
	executionFormsDatabaseFile    = "peen.db"

	executionFormsRestartMessage = "confirm persisted history"
	executionFormsReloadMessage  = "confirm updated rules"
	executionFormsUnauthMessage  = "no token required"

	executionFormsUpstreamName               = "integration"
	executionFormsUpstreamProvider           = "openai"
	executionFormsUpstreamNameKey            = "name"
	executionFormsUpstreamTypeKey            = "type"
	executionFormsUpstreamURLKey             = "baseUrl"
	executionFormsMessageKey                 = "message"
	executionFormsWebSocketMetadataSessionID = "sessionId"
	executionFormsWebSocketProtocol          = "peen.v1"
	executionFormsWebSocketBearerPrefix      = "peen.bearer." //nolint:gosec // A subprotocol prefix, not a credential.
	executionFormsWebSocketMessageSend       = "message.send"
	executionFormsWebSocketCompleted         = "message.completed"
	executionFormsWebSocketFailed            = "message.failed"
	executionFormsMetricFamily               = "peen_http_requests_total"
	executionFormsNetworkTCP                 = "tcp"
	executionFormsLoopbackAddress            = "127.0.0.1:0"
	executionFormsJSONToolMessage            = "run the JSON tool turn"
	executionFormsPatchToolMessage           = "run the patch tool turn"
	executionFormsPatchText                  = "*** Begin Patch\n*** Update File: patch-edit.txt\n@@\n-alpha\n+bravo\n*** End Patch"
	executionFormsToolPathKey                = "path"
	executionFormsToolEditsKey               = "edits"
	executionFormsToolOldTextKey             = "old"
	executionFormsToolNewTextKey             = "new"
	executionFormsToolPatchKey               = "patch"
	executionFormsToolCommandKey             = "command"
	executionFormsToolPurposeKey             = "purpose"
	executionFormsJSONPurpose                = "write the JSON command marker"
	executionFormsPatchPurpose               = "write the patch command marker"

	executionFormsAgentEnvironmentPrefix = "PEEN_AGENT="
	executionFormsAgentUnsetMessage      = "confirm the default agent"
	executionFormsCoreGeneralPurpose     = "core:general-purpose"

	// executionFormsGeneralPurposeInstructions is the body of the embedded
	// general-purpose agent. A turn with PEEN_AGENT unset must send it to the
	// provider.
	executionFormsGeneralPurposeInstructions = "Carry out the user's request within the resolved instructions and tool"

	executionFormsRefusalTimeout  = 20 * time.Second
	executionFormsRefusalExitCode = 1

	executionFormsRetentionNotDayMessage = "PEEN_LOG_RETENTION must be a " +
		"positive whole number of days (a multiple of 24h), got 36h0m0s"
	executionFormsInvalidDisabledSkillsMessage = "invalid PEEN_DISABLED_CORE_SKILLS"
	executionFormsValidCoreSkillsMessage       = "valid names are freshness, planning and all"
	executionFormsInvalidDisabledAgentsMessage = "invalid PEEN_DISABLED_CORE_AGENTS"
	executionFormsValidCoreAgentsMessage       = "valid names are general-purpose and all"
	executionFormsInvalidAgentMessage          = "invalid PEEN_AGENT"
	executionFormsDisabledCoreAgentMessage     = "is disabled by PEEN_DISABLED_CORE_AGENTS"
)

type executionForm struct {
	name  string
	build func(t *testing.T, repository string) string
}

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

func TestExecutionFormsExposeTheRestartSafeHTTPContract(t *testing.T) {
	requireNoDockerDaemon(t)

	repository := executionFormsRepositoryRoot(t)
	forms := []executionForm{
		{
			name:  executionFormsSourceForm,
			build: buildSourceForm,
		},
		{
			name:  executionFormsInstalledForm,
			build: buildInstalledForm,
		},
	}

	for _, form := range forms {
		t.Run(form.name, func(t *testing.T) {
			binary := form.build(t, repository)
			runAuthenticatedFormScenario(t, binary)
			runUnauthenticatedFormScenario(t, binary)
		})
	}
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

func TestExecutionFormsServeATurnWithTheDefaultAgent(t *testing.T) {
	requireNoDockerDaemon(t)

	binary := buildSourceForm(t, executionFormsRepositoryRoot(t))
	provider := testinfra.NewProviderMock()
	t.Cleanup(provider.Close)
	state := newProcessState(t)
	removeCustomDefaultAgent(t, state)

	process := startPeen(t, processConfig{
		binary:          binary,
		configDirectory: state.configDirectory,
		stateDirectory:  state.stateDirectory,
		workspace:       state.workspace,
		workerSocketDir: state.workerSocketDir,
		apiAddress:      reserveLoopbackAddress(t),
		metricsAddress:  reserveLoopbackAddress(t),
		provider:        provider,
		apiToken:        executionFormsAPIToken,
		unsetAgent:      true,
	})
	t.Cleanup(func() { process.stop(t) })

	sendWebSocketTurn(
		t,
		process.baseURL,
		executionFormsAPIToken,
		state.workspace,
		executionFormsAgentUnsetMessage,
	)

	assert.Contains(
		t,
		provider.LastSystemPrompt(),
		executionFormsGeneralPurposeInstructions,
	)
}

// removeCustomDefaultAgent deletes the config directory's custom default
// agent, so "default" has no definition and core:general-purpose is the only
// embedded agent.
func removeCustomDefaultAgent(t *testing.T, state processState) {
	t.Helper()

	require.NoError(t, os.Remove(filepath.Join(
		state.configDirectory,
		executionFormsAgentsDirectory,
		executionFormsAgentFile,
	)))
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

// fetchURL sends one GET and returns the status code and the whole body, with
// the response body closed.
func fetchURL(
	ctx context.Context,
	client *http.Client,
	target string,
) (int, []byte, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		target,
		nil,
	)
	if err != nil {
		return 0, nil, err
	}

	response, err := client.Do(request)
	if err != nil {
		return 0, nil, err
	}

	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()

	if err := errors.Join(readErr, closeErr); err != nil {
		return 0, nil, err
	}

	return response.StatusCode, body, nil
}

func buildSourceForm(t *testing.T, repository string) string {
	t.Helper()

	binary := filepath.Join(t.TempDir(), executionFormsSourceBinaryName)
	runBuildCommand(t, repository, nil, "go", "build", "-o", binary, executionFormsCommandPackage)

	return binary
}

func buildInstalledForm(t *testing.T, repository string) string {
	t.Helper()

	installDirectory := t.TempDir()
	runBuildCommand(
		t,
		repository,
		[]string{"GOBIN=" + installDirectory},
		"go",
		"install",
		executionFormsCommandPackage,
	)

	return filepath.Join(installDirectory, executionFormsInstalledBinaryName)
}

func runAuthenticatedFormScenario(t *testing.T, binary string) {
	t.Helper()

	provider := testinfra.NewProviderMock()
	t.Cleanup(provider.Close)
	state := newProcessState(t)
	process := startPeen(t, processConfig{
		binary:          binary,
		configDirectory: state.configDirectory,
		stateDirectory:  state.stateDirectory,
		workspace:       state.workspace,
		workerSocketDir: state.workerSocketDir,
		apiAddress:      reserveLoopbackAddress(t),
		metricsAddress:  reserveLoopbackAddress(t),
		provider:        provider,
		apiToken:        executionFormsAPIToken,
	})
	t.Cleanup(func() { process.stop(t) })

	assertUnauthorized(t, process.baseURL)

	provider.EnableScriptedToolTurn(jsonToolTurn())
	jsonSessionID := sendWebSocketTurn(
		t,
		process.baseURL,
		executionFormsAPIToken,
		state.workspace,
		executionFormsJSONToolMessage,
	)
	provider.DisableScriptedToolTurn()
	assertWorkspaceFile(t, state.workspace, executionFormsJSONFixtureFile, executionFormsJSONAfter)
	assertWorkspaceFile(t, state.workspace, executionFormsJSONCommandFile, executionFormsJSONOutput)

	provider.EnableScriptedToolTurn(patchToolTurn())
	sendWebSocketTurn(
		t,
		process.baseURL,
		executionFormsAPIToken,
		state.workspace,
		executionFormsPatchToolMessage,
	)
	provider.DisableScriptedToolTurn()
	assertWorkspaceFile(t, state.workspace, executionFormsPatchFixtureFile, executionFormsPatchAfter)
	assertWorkspaceFile(t, state.workspace, executionFormsPatchCommandFile, executionFormsPatchOutput)

	process.stop(t)
	process = startPeen(t, processConfig{
		binary:          binary,
		configDirectory: state.configDirectory,
		stateDirectory:  state.stateDirectory,
		workspace:       state.workspace,
		workerSocketDir: state.workerSocketDir,
		apiAddress:      reserveLoopbackAddress(t),
		metricsAddress:  reserveLoopbackAddress(t),
		provider:        provider,
		apiToken:        executionFormsAPIToken,
	})

	assertSessionHistory(t, process.baseURL, executionFormsAPIToken, jsonSessionID)
	writeFile(
		t,
		filepath.Join(state.workspace, executionFormsRulesFile),
		executionFormsUpdatedRules,
	)
	sendWebSocketTurn(
		t,
		process.baseURL,
		executionFormsAPIToken,
		state.workspace,
		executionFormsReloadMessage,
	)
	assert.Contains(t, provider.LastSystemPrompt(), executionFormsUpdatedRules)
	assertPrivateMetrics(t, process)

	// Durable state lives in the state directory, never in the configuration
	// directory a Docker worker receives read-only.
	assert.FileExists(
		t,
		filepath.Join(state.stateDirectory, executionFormsDatabaseFile),
	)
	assert.NoFileExists(
		t,
		filepath.Join(state.configDirectory, executionFormsDatabaseFile),
	)
}

func runUnauthenticatedFormScenario(t *testing.T, binary string) {
	t.Helper()

	provider := testinfra.NewProviderMock()
	t.Cleanup(provider.Close)
	state := newProcessState(t)
	process := startPeen(t, processConfig{
		binary:          binary,
		configDirectory: state.configDirectory,
		stateDirectory:  state.stateDirectory,
		workspace:       state.workspace,
		workerSocketDir: state.workerSocketDir,
		apiAddress:      reserveLoopbackAddress(t),
		metricsAddress:  reserveLoopbackAddress(t),
		provider:        provider,
	})
	t.Cleanup(func() { process.stop(t) })

	sendWebSocketTurn(
		t,
		process.baseURL,
		"",
		state.workspace,
		executionFormsUnauthMessage,
	)
}

func jsonToolTurn() testinfra.ScriptedToolTurn {
	return testinfra.ScriptedToolTurn{
		UserMessage: executionFormsJSONToolMessage,
		ReadFileArguments: map[string]any{
			executionFormsToolPathKey: executionFormsJSONFixtureFile,
		},
		EditFileArguments: map[string]any{
			executionFormsToolPathKey: executionFormsJSONFixtureFile,
			executionFormsToolEditsKey: []map[string]any{{
				executionFormsToolOldTextKey: "before",
				executionFormsToolNewTextKey: "after",
			}},
		},
		RunCommandArguments: map[string]any{
			executionFormsToolCommandKey: executionFormsJSONCommand,
			executionFormsToolPurposeKey: executionFormsJSONPurpose,
		},
		FinalAnswer: executionFormsJSONAnswer,
	}
}

func patchToolTurn() testinfra.ScriptedToolTurn {
	return testinfra.ScriptedToolTurn{
		UserMessage: executionFormsPatchToolMessage,
		ReadFileArguments: map[string]any{
			executionFormsToolPathKey: executionFormsPatchFixtureFile,
		},
		ApplyPatchArguments: map[string]any{
			executionFormsToolPatchKey: executionFormsPatchText,
		},
		RunCommandArguments: map[string]any{
			executionFormsToolCommandKey: executionFormsPatchCommand,
			executionFormsToolPurposeKey: executionFormsPatchPurpose,
		},
		FinalAnswer: executionFormsPatchAnswer,
	}
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

func assertUnauthorized(t *testing.T, baseURL string) {
	t.Helper()

	endpoint := executionFormsWebSocketURL(t, baseURL)
	dialer := websocket.Dialer{
		Subprotocols: []string{executionFormsWebSocketProtocol},
	}
	connection, response, err := dialer.Dial(endpoint, nil)
	require.Error(t, err)
	require.Nil(t, connection)
	require.NotNil(t, response)
	require.Equal(t, http.StatusUnauthorized, response.StatusCode)
	require.NoError(t, response.Body.Close())
}

func sendWebSocketTurn(
	t *testing.T,
	baseURL string,
	token string,
	workspace string,
	message string,
) uuid.UUID {
	t.Helper()

	routedSession := openExecutionFormsSession(t, baseURL, token, workspace)

	dialer := websocket.Dialer{
		HandshakeTimeout: executionFormsStartupTimeout,
		Subprotocols: []string{
			executionFormsWebSocketProtocol,
			executionFormsWebSocketBearerPrefix + base64.RawURLEncoding.EncodeToString(
				[]byte(token),
			),
		},
	}
	connection, response, err := dialer.Dial(
		executionFormsWebSocketURL(t, baseURL),
		nil,
	)
	if response != nil {
		t.Cleanup(func() { require.NoError(t, response.Body.Close()) })
	}
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	messageEvent := dabluveees.NewEvent(
		executionFormsWebSocketMessageSend,
		map[string]string{executionFormsMessageKey: message},
	).SetMetadata(
		executionFormsWebSocketMetadataSessionID,
		routedSession.String(),
	)
	require.NoError(t, connection.WriteJSON(messageEvent))

	deadline := time.Now().Add(executionFormsTurnTimeout)
	for {
		require.NoError(t, connection.SetReadDeadline(deadline))
		event := dabluveees.Event{}
		require.NoError(t, connection.ReadJSON(&event))
		switch string(event.Type) {
		case executionFormsWebSocketCompleted:
			require.NotNil(t, event.Metadata)
			sessionValue, found := event.Metadata.Get(
				executionFormsWebSocketMetadataSessionID,
			)
			require.True(t, found)
			sessionText, ok := sessionValue.(string)
			require.True(t, ok)
			sessionID, parseErr := uuid.Parse(sessionText)
			require.NoError(t, parseErr)

			return sessionID
		case executionFormsWebSocketFailed:
			t.Fatalf("WebSocket message failed: %s", event.Data)
		}
	}
}

func executionFormsWebSocketURL(
	t *testing.T,
	baseURL string,
) string {
	t.Helper()

	endpoint, err := url.Parse(baseURL + executionFormsWebSocketPath)
	require.NoError(t, err)
	endpoint.Scheme = "ws"

	return endpoint.String()
}

// openExecutionFormsSession opens the process's workspace through the real
// control endpoint. Peen starts with no sessions in every execution form, so a
// turn has to name the session it belongs to.
func openExecutionFormsSession(
	t *testing.T,
	baseURL string,
	token string,
	workspace string,
) uuid.UUID {
	t.Helper()

	body, err := json.Marshal(map[string]string{"workspace": workspace})
	require.NoError(t, err)

	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		baseURL+executionFormsAPIOpenSessionPath,
		bytes.NewReader(body),
	)
	require.NoError(t, err)

	// A deployment with no configured token authenticates nothing, and the
	// unauthenticated scenario runs that way. Sending an empty bearer instead
	// of no header would be a malformed credential rather than none.
	if token != "" {
		request.Header.Set(
			executionFormsHeaderAuthorization,
			executionFormsBearerPrefix+token,
		)
	}

	request.Header.Set(
		executionFormsHeaderContentType,
		executionFormsContentTypeJSON,
	)

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)

	payload, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	require.NoError(t, readErr)
	require.NoError(t, closeErr)
	require.Equal(t, http.StatusOK, response.StatusCode, string(payload))

	opened := struct {
		Session struct {
			ID        uuid.UUID `json:"id"`
			Workspace string    `json:"workspace"`
		} `json:"session"`
	}{}
	require.NoError(t, json.Unmarshal(payload, &opened))

	return opened.Session.ID
}

func assertSessionHistory(
	t *testing.T,
	baseURL string,
	token string,
	sessionID uuid.UUID,
) {
	t.Helper()

	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		baseURL+executionFormsAPIMessagesPath,
		nil,
	)
	require.NoError(t, err)
	request.Header.Set(executionFormsHeaderSessionID, sessionID.String())
	request.Header.Set(
		executionFormsHeaderAuthorization,
		executionFormsBearerPrefix+token,
	)

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	require.NoError(t, readErr)
	require.NoError(t, closeErr)
	assert.Contains(t, string(body), executionFormsJSONAnswer)
}

func assertPrivateMetrics(t *testing.T, process *runningPeen) {
	t.Helper()

	publicRequest, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		process.baseURL+executionFormsMetricsPath,
		nil,
	)
	require.NoError(t, err)
	publicRequest.Header.Set(
		executionFormsHeaderAuthorization,
		executionFormsBearerPrefix+executionFormsAPIToken,
	)
	publicResponse, err := http.DefaultClient.Do(publicRequest)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, publicResponse.StatusCode)
	require.NoError(t, publicResponse.Body.Close())

	status, metricsBody, err := fetchURL(
		t.Context(),
		http.DefaultClient,
		process.metricsURL+executionFormsMetricsPath,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, string(metricsBody), executionFormsMetricFamily)
}

func assertWorkspaceFile(t *testing.T, workspace string, name string, want string) {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(workspace, name))
	require.NoError(t, err)
	assert.Equal(t, want, string(content))
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), executionFormsDirectoryMode))
	require.NoError(t, os.WriteFile(path, []byte(content), executionFormsFileMode))
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

func runBuildCommand(
	t *testing.T,
	directory string,
	environment []string,
	name string,
	arguments ...string,
) {
	t.Helper()

	command := exec.CommandContext(t.Context(), name, arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), environment...)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}

func executionFormsRepositoryRoot(t *testing.T) string {
	t.Helper()

	directory, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, statErr := os.Stat(
			filepath.Join(directory, executionFormsRepositoryModuleFile),
		); statErr == nil {
			return directory
		}

		parent := filepath.Dir(directory)
		if parent == directory {
			require.Fail(t, "repository root not found")

			return ""
		}
		directory = parent
	}
}
