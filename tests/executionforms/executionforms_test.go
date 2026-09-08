//go:build integration

package executionforms

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	executionFormsAPIMessagesPath = "/v1/messages"
	executionFormsReadyPath       = "/ready"
	executionFormsMetricsPath     = "/metrics"

	executionFormsHeaderAuthorization = "Authorization"
	executionFormsHeaderSessionID     = "X-Session-ID"
	executionFormsHeaderAccept        = "Accept"
	executionFormsHeaderContentType   = "Content-Type"

	executionFormsBearerPrefix    = "Bearer "
	executionFormsJSONMediaType   = "application/json"
	executionFormsSSEMediaType    = "text/event-stream"
	executionFormsAgentName       = "default"
	executionFormsAPIToken        = "EXAMPLE-DO-NOT-USE"
	executionFormsDirectoryMode   = 0o700
	executionFormsFileMode        = 0o600
	executionFormsStartupTimeout  = 30 * time.Second
	executionFormsShutdownTimeout = 15 * time.Second
	executionFormsPollInterval    = 10 * time.Millisecond

	executionFormsAgentDocument = "---\nname: default\ndescription: execution form test agent\n---\nFollow the test instructions."
	executionFormsInitialRules  = "Initial workspace rule."
	executionFormsUpdatedRules  = "Updated workspace rule."

	executionFormsJSONFixtureFile = "json-edit.txt"
	executionFormsSSEFixtureFile  = "sse-patch.txt"
	executionFormsJSONCommandFile = "json-command.txt"
	executionFormsSSECommandFile  = "sse-command.txt"

	executionFormsJSONBefore = "before\n"
	executionFormsJSONAfter  = "after\n"
	executionFormsSSEBefore  = "alpha\n"
	executionFormsSSEAfter   = "bravo\n"

	executionFormsJSONCommand = "printf 'json command\\n' > json-command.txt"
	executionFormsSSECommand  = "printf 'sse command\\n' > sse-command.txt"
	executionFormsJSONOutput  = "json command\n"
	executionFormsSSEOutput   = "sse command\n"

	executionFormsJSONAnswer = "json tool turn complete"
	executionFormsSSEAnswer  = "sse tool turn complete"

	executionFormsSourceBinaryName    = "peen-source"
	executionFormsInstalledBinaryName = "cmd"
	executionFormsSourceForm          = "source build"
	executionFormsInstalledForm       = "go install"

	executionFormsRepositoryModuleFile = "go.mod"
	executionFormsCommandPackage       = "./cmd"
	executionFormsRunCommand           = "run"

	executionFormsConfigDirectory = "config"
	executionFormsWorkspace       = "workspace"
	executionFormsAgentsDirectory = ".agents/agents"
	executionFormsAgentFile       = "default.md"
	executionFormsRulesFile       = "AGENTS.md"
	executionFormsDatabaseFile    = "peen.db"

	executionFormsRestartMessage = "confirm persisted history"
	executionFormsReloadMessage  = "confirm updated rules"
	executionFormsUnauthMessage  = "no token required"

	executionFormsUpstreamName     = "integration"
	executionFormsUpstreamProvider = "openai"
	executionFormsUpstreamNameKey  = "name"
	executionFormsUpstreamTypeKey  = "provider"
	executionFormsUpstreamURLKey   = "baseUrl"
	executionFormsMessageKey       = "message"
	executionFormsMetricFamily     = "peen_http_requests_total"
	executionFormsNetworkTCP       = "tcp"
	executionFormsLoopbackAddress  = "127.0.0.1:0"
	executionFormsMissingToken     = "missing token"
	executionFormsJSONToolMessage  = "run the JSON tool turn"
	executionFormsSSEToolMessage   = "run the SSE tool turn"
	executionFormsPatchText        = "*** Begin Patch\n*** Update File: sse-patch.txt\n@@\n-alpha\n+bravo\n*** End Patch"
	executionFormsToolPathKey      = "path"
	executionFormsToolEditsKey     = "edits"
	executionFormsToolOldTextKey   = "old"
	executionFormsToolNewTextKey   = "new"
	executionFormsToolPatchKey     = "patch"
	executionFormsToolCommandKey   = "command"
	executionFormsToolPurposeKey   = "purpose"
	executionFormsJSONPurpose      = "write the JSON command marker"
	executionFormsSSEPurpose       = "write the SSE command marker"
)

type executionForm struct {
	name  string
	build func(t *testing.T, repository string) string
}

type runningPeen struct {
	command    *exec.Cmd
	done       chan error
	output     bytes.Buffer
	baseURL    string
	metricsURL string
	stopped    bool
}

type processConfig struct {
	binary          string
	configDirectory string
	workspace       string
	apiAddress      string
	metricsAddress  string
	provider        *testinfra.ProviderMock
	apiToken        string
}

func TestExecutionFormsExposeTheRestartSafeHTTPContract(t *testing.T) {
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
		workspace:       state.workspace,
		apiAddress:      reserveLoopbackAddress(t),
		metricsAddress:  reserveLoopbackAddress(t),
		provider:        provider,
		apiToken:        executionFormsAPIToken,
	})
	t.Cleanup(func() { process.stop(t) })

	assertUnauthorized(t, process.baseURL)

	provider.EnableScriptedToolTurn(jsonToolTurn())
	jsonSessionID := sendJSONTurn(
		t,
		process.baseURL,
		executionFormsAPIToken,
		uuid.Nil,
		executionFormsJSONToolMessage,
	)
	provider.DisableScriptedToolTurn()
	assertWorkspaceFile(t, state.workspace, executionFormsJSONFixtureFile, executionFormsJSONAfter)
	assertWorkspaceFile(t, state.workspace, executionFormsJSONCommandFile, executionFormsJSONOutput)

	provider.EnableScriptedToolTurn(sseToolTurn())
	sendSSETurn(
		t,
		process.baseURL,
		executionFormsAPIToken,
		executionFormsSSEToolMessage,
	)
	provider.DisableScriptedToolTurn()
	assertWorkspaceFile(t, state.workspace, executionFormsSSEFixtureFile, executionFormsSSEAfter)
	assertWorkspaceFile(t, state.workspace, executionFormsSSECommandFile, executionFormsSSEOutput)

	process.stop(t)
	process = startPeen(t, processConfig{
		binary:          binary,
		configDirectory: state.configDirectory,
		workspace:       state.workspace,
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
	sendJSONTurn(
		t,
		process.baseURL,
		executionFormsAPIToken,
		jsonSessionID,
		executionFormsReloadMessage,
	)
	assert.Contains(t, provider.LastSystemPrompt(), executionFormsUpdatedRules)
	assertPrivateMetrics(t, process)
	assert.FileExists(t, filepath.Join(state.configDirectory, executionFormsDatabaseFile))
}

func runUnauthenticatedFormScenario(t *testing.T, binary string) {
	t.Helper()

	provider := testinfra.NewProviderMock()
	t.Cleanup(provider.Close)
	state := newProcessState(t)
	process := startPeen(t, processConfig{
		binary:          binary,
		configDirectory: state.configDirectory,
		workspace:       state.workspace,
		apiAddress:      reserveLoopbackAddress(t),
		metricsAddress:  reserveLoopbackAddress(t),
		provider:        provider,
	})
	t.Cleanup(func() { process.stop(t) })

	sendJSONTurn(
		t,
		process.baseURL,
		"",
		uuid.Nil,
		executionFormsUnauthMessage,
	)
}

func jsonToolTurn() testinfra.ScriptedToolTurn {
	return testinfra.ScriptedToolTurn{
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

func sseToolTurn() testinfra.ScriptedToolTurn {
	return testinfra.ScriptedToolTurn{
		ReadFileArguments: map[string]any{
			executionFormsToolPathKey: executionFormsSSEFixtureFile,
		},
		ApplyPatchArguments: map[string]any{
			executionFormsToolPatchKey: executionFormsPatchText,
		},
		RunCommandArguments: map[string]any{
			executionFormsToolCommandKey: executionFormsSSECommand,
			executionFormsToolPurposeKey: executionFormsSSEPurpose,
		},
		FinalAnswer: executionFormsSSEAnswer,
	}
}

type processState struct {
	configDirectory string
	workspace       string
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
		filepath.Join(workspace, executionFormsSSEFixtureFile),
		executionFormsSSEBefore,
	)

	return processState{
		configDirectory: configDirectory,
		workspace:       workspace,
	}
}

func startPeen(t *testing.T, config processConfig) *runningPeen {
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
	process.command = exec.Command(config.binary, executionFormsRunCommand)
	process.command.Env = peenEnvironment(config, string(upstreams))
	process.command.Stdout = &process.output
	process.command.Stderr = &process.output

	require.NoError(t, process.command.Start())
	go func() {
		process.done <- process.command.Wait()
	}()

	awaitReady(t, process)

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
		"PEEN_WORKING_DIR="+config.workspace,
		"PEEN_AGENT="+executionFormsAgentName,
		"PEEN_HTTP_LISTEN_ADDRESS="+config.apiAddress,
		"PEEN_METRICS_LISTEN_ADDRESS="+config.metricsAddress,
		"PEEN_UPSTREAMS="+upstreams,
		"PEEN_DEFAULT_MODEL="+config.provider.DefaultModel(),
		"PEEN_API_TOKEN="+config.apiToken,
	)

	return environment
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
		response, err := client.Get(process.baseURL + executionFormsReadyPath)
		if err == nil {
			closeErr := response.Body.Close()
			if response.StatusCode == http.StatusOK && closeErr == nil {
				return
			}
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

	response := postMessage(
		t,
		baseURL,
		"",
		uuid.Nil,
		executionFormsMissingToken,
		"",
	)
	require.Equal(t, http.StatusUnauthorized, response.StatusCode)
	require.NoError(t, response.Body.Close())
}

func sendJSONTurn(
	t *testing.T,
	baseURL string,
	token string,
	sessionID uuid.UUID,
	message string,
) uuid.UUID {
	t.Helper()

	response := postMessage(t, baseURL, token, sessionID, message, "")
	require.Equal(t, http.StatusOK, response.StatusCode)
	responseBody, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	require.NoError(t, readErr)
	require.NoError(t, closeErr)
	assert.Contains(t, string(responseBody), "message")

	return responseSessionID(t, response)
}

func sendSSETurn(
	t *testing.T,
	baseURL string,
	token string,
	message string,
) {
	t.Helper()

	response := postMessage(
		t,
		baseURL,
		token,
		uuid.Nil,
		message,
		executionFormsSSEMediaType,
	)
	require.Equal(t, http.StatusOK, response.StatusCode)
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	require.NoError(t, readErr)
	require.NoError(t, closeErr)
	assert.Contains(t, string(body), "message_stop")
}

func postMessage(
	t *testing.T,
	baseURL string,
	token string,
	sessionID uuid.UUID,
	message string,
	accept string,
) *http.Response {
	t.Helper()

	payload, err := json.Marshal(map[string]string{
		executionFormsMessageKey: message,
	})
	require.NoError(t, err)
	request, err := http.NewRequest(
		http.MethodPost,
		baseURL+executionFormsAPIMessagesPath,
		bytes.NewReader(payload),
	)
	require.NoError(t, err)
	request.Header.Set(executionFormsHeaderContentType, executionFormsJSONMediaType)
	if token != "" {
		request.Header.Set(
			executionFormsHeaderAuthorization,
			executionFormsBearerPrefix+token,
		)
	}
	if sessionID != uuid.Nil {
		request.Header.Set(executionFormsHeaderSessionID, sessionID.String())
	}
	if accept != "" {
		request.Header.Set(executionFormsHeaderAccept, accept)
	}

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)

	return response
}

func responseSessionID(t *testing.T, response *http.Response) uuid.UUID {
	t.Helper()

	sessionID, err := uuid.Parse(response.Header.Get(executionFormsHeaderSessionID))
	require.NoError(t, err)

	return sessionID
}

func assertSessionHistory(
	t *testing.T,
	baseURL string,
	token string,
	sessionID uuid.UUID,
) {
	t.Helper()

	request, err := http.NewRequest(
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

	publicRequest, err := http.NewRequest(
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

	privateResponse, err := http.Get(process.metricsURL + executionFormsMetricsPath)
	require.NoError(t, err)
	metricsBody, readErr := io.ReadAll(privateResponse.Body)
	closeErr := privateResponse.Body.Close()
	require.NoError(t, readErr)
	require.NoError(t, closeErr)
	require.Equal(t, http.StatusOK, privateResponse.StatusCode)
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
