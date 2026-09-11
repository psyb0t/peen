// Package testinfra is the extendable integration-test harness for a
// servicepack app. Its baseline builds the servicepack image from the repo
// Dockerfile and runs it, so a test exercises the real, containerized
// application with whatever services it registers, and nothing more.
//
// Extend it with the external dependencies your services need (a database, a
// cache, a broker via testcontainers-go): give Infra a field, start it in
// Setup, wire the app container to it, and tear it down in Teardown. The DIND
// runner already supports this: DEV_RUN_DIND uses the host network, so
// testcontainers' host-published ports are reachable.
package testinfra

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/psyb0t/ctxerrors"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	appReadyLog                = "Starting HTTP server on"
	appDockerfile              = "Dockerfile"
	appBootTimeout             = 5 * time.Minute
	appConfigDirectory         = "/tmp/peen"
	appWorkingDirectory        = "/tmp"
	appAgentName               = "default"
	appFixtureInstructionsPath = "/tmp/peen-test-AGENTS.md"
	appFixtureAgentPath        = "/tmp/peen-test-default.md"
	appFixtureFileMode         = 0o644
	appFixtureDirectoryMode    = 0o755
	appRootInstructions        = "Follow the request and use available tools."
	//nolint:lll // Fixture content is byte exact and has no trailing newline.
	appAgentDocument    = "---\nname: default\ndescription: API integration test agent\n---\nFollow the request and return the result."
	appBootstrapCommand = `mkdir -p /tmp/peen/.agents/agents
cp /tmp/peen-test-AGENTS.md /tmp/peen/AGENTS.md
cp /tmp/peen-test-default.md /tmp/peen/.agents/agents/default.md
exec /app/app run`
	appNetwork                = "tcp"
	appHostNetwork            = "host"
	appLoopbackAddress        = "127.0.0.1:0"
	appURLPrefix              = "http://"
	appReadyPath              = "/ready"
	appRequestTimeout         = 30 * time.Second
	appRestartTimeout         = 30 * time.Second
	appRestartPollInterval    = 10 * time.Millisecond
	appCoverageDirectory      = "/tmp/peen-coverage"
	appCoverageBuildArgument  = "PEEN_ENABLE_COVERAGE"
	appCoverageEnabled        = "true"
	appCoverageEnvironment    = "GOCOVERDIR"
	coverageDirectoryEnv      = "SERVICEPACK_COVDATA_DIR"
	providerName              = "integration"
	providerType              = "openai"
	providerModel             = "test-model"
	providerModelsPath        = "/models"
	providerModelsPathV1      = "/v1/models"
	providerCompletionsPath   = "/chat/completions"
	providerCompletionsPathV1 = "/v1/chat/completions"
	providerObjectField       = "object"
	providerModelsObjectType  = "list"
	providerModelObjectType   = "model"
	providerChunkObjectType   = "chat.completion.chunk"
	providerCompletionID      = "chatcmpl-integration"
	providerResponsePrefix    = "integration completion "
	providerCreatedAt         = 1
	jsonMediaType             = "application/json"
	eventStreamMediaType      = "text/event-stream"
	openAIDataPrefix          = "data: "
	openAIDoneEvent           = "data: [DONE]\n\n"
	// TestAPIToken is the deliberately fake bearer token injected into the
	// production container for API integration tests.
	TestAPIToken = "EXAMPLE-DO-NOT-USE"

	// providerRoleSystem, providerRoleAssistant, and providerRoleTool are the
	// transcript roles the OpenAI wire format uses for fixture messages.
	// A scripted turn counts tool results to select its current round.
	providerRoleSystem    = "system"
	providerRoleAssistant = "assistant"
	providerRoleTool      = "tool"

	// These names match internal/pkg/agent/tools.go's registrations.
	scriptedToolNameReadFile   = "read_file"
	scriptedToolNameEditFile   = "edit_file"
	scriptedToolNameApplyPatch = "apply_patch"
	scriptedToolNameRunCommand = "run_command"

	// These fixed call IDs let tests pair durable tool messages and SSE blocks.
	ScriptedCallIDReadFile   = "call-scripted-read-file"
	ScriptedCallIDEditFile   = "call-scripted-edit-file"
	ScriptedCallIDApplyPatch = "call-scripted-apply-patch"
	ScriptedCallIDRunCommand = "call-scripted-run-command"

	// scriptedRoundReadFile, scriptedRoundEditFile, and
	// scriptedRoundRunCommand are the tool-message counts that select each
	// scripted step; any higher count answers with plain text.
	scriptedRoundReadFile   = 0
	scriptedRoundEditFile   = 1
	scriptedRoundRunCommand = 2

	openAIToolCallType          = "function"
	openAIDeltaFieldRole        = "role"
	openAIDeltaFieldToolCalls   = "tool_calls"
	openAIDeltaReasoningContent = "reasoning_content"
	openAIFinishReasonStop      = "stop"
	openAIFinishReasonToolCalls = "tool_calls"
	openAIFieldIndex            = "index"
	openAIFieldDelta            = "delta"
	openAIFieldFinishReason     = "finish_reason"

	// ContainerWorkingDirectory mirrors appWorkingDirectory (PEEN_WORKING_DIR)
	// so a test can seed and read back fixture files under the container's
	// default tool workspace without duplicating the path.
	ContainerWorkingDirectory = appWorkingDirectory

	// DefaultProviderReasoning is the visible reasoning emitted by ordinary
	// provider completions in API integration tests.
	DefaultProviderReasoning = "integration reasoning"
)

var errNoGoMod = errors.New("go.mod not found above the working directory")

// Infra holds the containers a test package brought up. At the baseline that is
// just the application image; extend it with one field per external dependency
// (for example: Postgres *PostgresResource) as your services grow.
type Infra struct {
	App        testcontainers.Container
	baseURL    string
	metricsURL string
	client     *http.Client
	provider   *openAIModelsMock
}

type appCoverage struct {
	hostDirectory string
}

// Setup builds Peen's app image and starts it against a local OpenAI-compatible
// provider mock. API readiness proves production config, migrations, provider
// discovery, and the HTTP service all completed.
func Setup(ctx context.Context) (*Infra, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, err
	}

	provider := newOpenAIModelsMock()

	listenAddress, err := reserveLoopbackAddress(ctx)
	if err != nil {
		return setupFailure(provider, err)
	}

	metricsListenAddress, err := reserveLoopbackAddress(ctx)
	if err != nil {
		return setupFailure(provider, err)
	}

	return startApp(
		ctx,
		root,
		provider,
		listenAddress,
		metricsListenAddress,
	)
}

func startApp(
	ctx context.Context,
	root string,
	provider *openAIModelsMock,
	listenAddress string,
	metricsListenAddress string,
) (*Infra, error) {
	environment, err := appEnvironment(
		provider.server.URL,
		listenAddress,
		metricsListenAddress,
	)
	if err != nil {
		return setupFailure(provider, err)
	}

	coverage, err := appCoverageFor(root)
	if err != nil {
		return setupFailure(provider, err)
	}

	container, err := testcontainers.GenericContainer(ctx,
		testcontainers.GenericContainerRequest{
			ContainerRequest: appContainerRequest(root, environment, coverage),
			Started:          true,
		},
	)
	if err != nil {
		return failedContainerSetup(ctx, container, provider, err)
	}

	return &Infra{
		App:        container,
		baseURL:    appURLPrefix + listenAddress,
		metricsURL: appURLPrefix + metricsListenAddress,
		client:     &http.Client{Timeout: appRequestTimeout},
		provider:   provider,
	}, nil
}

func appContainerRequest(
	root string,
	environment map[string]string,
	coverage appCoverage,
) testcontainers.ContainerRequest {
	request := testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context:    root,
			Dockerfile: appDockerfile,
			KeepImage:  false,
		},
		Entrypoint: []string{"/bin/sh", "-ceu", appBootstrapCommand},
		Env:        environment,
		Files: []testcontainers.ContainerFile{
			{
				Reader:            strings.NewReader(appRootInstructions),
				ContainerFilePath: appFixtureInstructionsPath,
				FileMode:          appFixtureFileMode,
			},
			{
				Reader:            strings.NewReader(appAgentDocument),
				ContainerFilePath: appFixtureAgentPath,
				FileMode:          appFixtureFileMode,
			},
		},
		NetworkMode: container.NetworkMode(appHostNetwork),
		WaitingFor: wait.ForLog(appReadyLog).
			WithStartupTimeout(appBootTimeout),
	}
	if coverage.hostDirectory == "" {
		return request
	}

	coverageBuildValue := appCoverageEnabled
	request.BuildArgs = map[string]*string{
		appCoverageBuildArgument: &coverageBuildValue,
	}
	request.Env[appCoverageEnvironment] = appCoverageDirectory
	request.HostConfigModifier = func(hostConfig *container.HostConfig) {
		hostConfig.NetworkMode = container.NetworkMode(appHostNetwork)
		hostConfig.Binds = append(
			hostConfig.Binds,
			coverage.hostDirectory+":"+appCoverageDirectory,
		)
	}
	request.User = strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())

	return request
}

func appCoverageFor(root string) (appCoverage, error) {
	configuredDirectory := os.Getenv(coverageDirectoryEnv)
	if configuredDirectory == "" {
		return appCoverage{}, nil
	}

	rootDirectory, err := filepath.Abs(root)
	if err != nil {
		return appCoverage{}, ctxerrors.Wrap(err, "resolve application root")
	}

	coverageDirectory, err := filepath.Abs(configuredDirectory)
	if err != nil {
		return appCoverage{}, ctxerrors.Wrap(err, "resolve coverage directory")
	}

	relativeDirectory, err := filepath.Rel(rootDirectory, coverageDirectory)
	if err != nil {
		return appCoverage{}, ctxerrors.Wrap(
			err,
			"check coverage directory scope",
		)
	}

	if relativeDirectory == ".." || strings.HasPrefix(
		relativeDirectory,
		".."+string(filepath.Separator),
	) {
		return appCoverage{}, ctxerrors.New(
			"coverage directory must stay within the application root",
		)
	}

	info, err := os.Stat(coverageDirectory)
	if err != nil {
		return appCoverage{}, ctxerrors.Wrap(err, "inspect coverage directory")
	}

	if !info.IsDir() {
		return appCoverage{}, ctxerrors.New("coverage path must be a directory")
	}

	return appCoverage{hostDirectory: coverageDirectory}, nil
}

func setupFailure(provider *openAIModelsMock, err error) (*Infra, error) {
	provider.server.Close()

	return nil, err
}

func failedContainerSetup(
	ctx context.Context,
	container testcontainers.Container,
	provider *openAIModelsMock,
	err error,
) (*Infra, error) {
	var cleanupErr error

	if container != nil {
		if terminateErr := container.Terminate(ctx); terminateErr != nil {
			cleanupErr = ctxerrors.Wrap(
				terminateErr,
				"terminate failed app container",
			)
		}
	}

	provider.server.Close()

	return nil, errors.Join(
		ctxerrors.Wrap(err, "build and start servicepack image"),
		cleanupErr,
	)
}

// Teardown terminates every started container. It is safe to call after a
// partially completed setup and returns any application cleanup failure.
func (i *Infra) Teardown(ctx context.Context) error {
	var teardownErr error

	if i.App != nil {
		if err := i.App.Terminate(ctx); err != nil {
			teardownErr = ctxerrors.Wrap(err, "terminate app container")
		}
	}

	if i.provider != nil {
		i.provider.server.Close()
	}

	return teardownErr
}

// Restart stops and starts the production application container, then waits
// for its already-configured HTTP listener to become ready again.
func (i *Infra) Restart(ctx context.Context) error {
	if i == nil || i.App == nil {
		return ctxerrors.New("application container is unavailable")
	}

	timeout := appRestartTimeout
	if err := i.App.Stop(ctx, &timeout); err != nil {
		return ctxerrors.Wrap(err, "stop application container")
	}

	if err := i.App.Start(ctx); err != nil {
		return ctxerrors.Wrap(err, "start application container")
	}

	return i.waitForReady(ctx)
}

// WriteWorkspaceFile adds or replaces one relative fixture file below the
// production container's configured default workspace.
func (i *Infra) WriteWorkspaceFile(
	ctx context.Context,
	name string,
	content []byte,
) error {
	if i == nil || i.App == nil {
		return ctxerrors.New("application container is unavailable")
	}

	target, err := workspaceFilePath(name)
	if err != nil {
		return err
	}

	if err := i.App.CopyToContainer(
		ctx,
		content,
		target,
		appFixtureFileMode,
	); err != nil {
		return ctxerrors.Wrap(err, "write application workspace fixture")
	}

	return nil
}

// LastSystemPrompt returns the most recent provider request's system prompt.
func (i *Infra) LastSystemPrompt() string {
	if i == nil || i.provider == nil {
		return ""
	}

	return i.provider.systemPrompt()
}

// ModelDiscoveryObserved reports whether the app called the configured models
// endpoint during startup.
func (i *Infra) ModelDiscoveryObserved() bool {
	return i.provider != nil && i.provider.modelsListed.Load()
}

// ScriptedToolTurn drives read_file, one mutation, run_command, and a final
// answer. Set exactly one of EditFileArguments and ApplyPatchArguments.
type ScriptedToolTurn struct {
	ReadFileArguments   map[string]any
	EditFileArguments   map[string]any
	ApplyPatchArguments map[string]any
	RunCommandArguments map[string]any
	FinalAnswer         string
}

// EnableScriptedToolTurn makes every subsequent provider completion follow
// script's fixed tool conversation instead
// of the default single fixed text response. Off by default, so a test that
// never calls this sees byte-identical behavior to before this method
// existed. Call DisableScriptedToolTurn (for example via t.Cleanup) once the
// scripted conversation is over, so later tests see the default behavior
// again.
func (i *Infra) EnableScriptedToolTurn(script ScriptedToolTurn) {
	i.provider.setScript(&script)
}

// DisableScriptedToolTurn restores the default fixed-text completion.
func (i *Infra) DisableScriptedToolTurn() {
	i.provider.setScript(nil)
}

// APIURL returns the production API endpoint reachable from the Go test
// process.
func (i *Infra) APIURL(path string) string {
	return i.baseURL + path
}

func (i *Infra) waitForReady(ctx context.Context) error {
	for {
		request, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			i.APIURL(appReadyPath),
			nil,
		)
		if err != nil {
			return ctxerrors.Wrap(err, "create readiness request")
		}

		response, requestErr := i.client.Do(request)
		if requestErr == nil {
			closeErr := response.Body.Close()
			if response.StatusCode == http.StatusOK && closeErr == nil {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctxerrors.Wrap(ctx.Err(), "wait for application readiness")
		case <-time.After(appRestartPollInterval):
		}
	}
}

func workspaceFilePath(name string) (string, error) {
	cleaned := filepath.Clean(name)
	if cleaned == "." || cleaned == ".." || filepath.IsAbs(cleaned) ||
		strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", ctxerrors.New("workspace fixture path must be relative")
	}

	return filepath.Join(appWorkingDirectory, cleaned), nil
}

// MetricsURL returns the private metrics endpoint reachable from the Go test
// process over the application container's loopback listener.
func (i *Infra) MetricsURL(path string) string {
	return i.metricsURL + path
}

// HTTPClient returns the client used to drive the production API container.
func (i *Infra) HTTPClient() *http.Client {
	return i.client
}

// AppLogs returns the current production container logs for failed API-test
// diagnostics. Test code must use them only to explain a failed contract.
func (i *Infra) AppLogs(ctx context.Context) (string, error) {
	if i.App == nil {
		return "", ctxerrors.New("app container is unavailable")
	}

	reader, err := i.App.Logs(ctx)
	if err != nil {
		return "", ctxerrors.Wrap(err, "read app container logs")
	}

	data, readErr := io.ReadAll(reader)

	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		return "", errors.Join(
			ctxerrors.Wrap(readErr, "read app container logs"),
			ctxerrors.Wrap(closeErr, "close app container logs"),
		)
	}

	return string(data), nil
}

// HoldNextCompletion blocks one provider completion until Release is called or
// the application cancels the upstream request.
func (i *Infra) HoldNextCompletion() (*CompletionHold, error) {
	if i.provider == nil {
		return nil, ctxerrors.New("provider mock is unavailable")
	}

	return i.provider.HoldNextCompletion()
}

// CompletionHold exposes the point where a provider completion has reached the
// mock and a release operation for tests that need an active agent turn.
type CompletionHold struct {
	Observed <-chan struct{}
	release  func()
}

// Release allows the held provider completion to emit its deterministic stream.
func (h *CompletionHold) Release() {
	if h == nil || h.release == nil {
		return
	}

	h.release()
}

type openAIModelsMock struct {
	server           *httptest.Server
	modelsListed     atomic.Bool
	completionCount  atomic.Int64
	holdMu           sync.Mutex
	nextHold         *completionHold
	scriptMu         sync.RWMutex
	script           *ScriptedToolTurn
	promptMu         sync.RWMutex
	lastSystemPrompt string
}

// ProviderMock is a deterministic local OpenAI-compatible provider for
// process-form tests. It exposes no credentials and accepts only Peen's fixed
// integration model.
type ProviderMock struct {
	mock *openAIModelsMock
}

// NewProviderMock starts a local deterministic provider fixture.
func NewProviderMock() *ProviderMock {
	return &ProviderMock{mock: newOpenAIModelsMock()}
}

// BaseURL returns the fixture's OpenAI-compatible endpoint.
func (m *ProviderMock) BaseURL() string {
	if m == nil || m.mock == nil || m.mock.server == nil {
		return ""
	}

	return m.mock.server.URL
}

// DefaultModel returns the qualified model reference accepted by the fixture.
func (m *ProviderMock) DefaultModel() string {
	return providerName + "/" + providerModel
}

// EnableScriptedToolTurn makes later completions issue the supplied tool
// sequence before returning its final answer.
func (m *ProviderMock) EnableScriptedToolTurn(script ScriptedToolTurn) {
	if m == nil || m.mock == nil {
		return
	}

	m.mock.setScript(&script)
}

// DisableScriptedToolTurn restores the fixture's ordinary text response.
func (m *ProviderMock) DisableScriptedToolTurn() {
	if m == nil || m.mock == nil {
		return
	}

	m.mock.setScript(nil)
}

// LastSystemPrompt returns the latest completion request's system prompt.
func (m *ProviderMock) LastSystemPrompt() string {
	if m == nil || m.mock == nil {
		return ""
	}

	return m.mock.systemPrompt()
}

// Close stops the fixture server.
func (m *ProviderMock) Close() {
	if m == nil || m.mock == nil || m.mock.server == nil {
		return
	}

	m.mock.server.Close()
}

func (m *openAIModelsMock) setScript(script *ScriptedToolTurn) {
	m.scriptMu.Lock()
	defer m.scriptMu.Unlock()

	m.script = script
}

func (m *openAIModelsMock) currentScript() *ScriptedToolTurn {
	m.scriptMu.RLock()
	defer m.scriptMu.RUnlock()

	return m.script
}

type completionHold struct {
	observed    chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

type providerCompletionRequest struct {
	Model    string                      `json:"model"`
	Stream   bool                        `json:"stream"`
	Messages []providerCompletionMessage `json:"messages"`
}

// providerCompletionMessage carries only the field a scripted turn needs:
// whether this transcript entry already carries a tool result.
type providerCompletionMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// toolMessageCount reports how many transcript messages already carry a
// tool role. A scripted turn treats that count as the round number, since
// each round it drives produces exactly one tool message.
func (r providerCompletionRequest) toolMessageCount() int {
	count := 0

	for _, message := range r.Messages {
		if message.Role == providerRoleTool {
			count++
		}
	}

	return count
}

func (r providerCompletionRequest) systemPrompt() string {
	for _, message := range r.Messages {
		if message.Role != providerRoleSystem {
			continue
		}

		var content string
		if err := json.Unmarshal(message.Content, &content); err == nil {
			return content
		}
	}

	return ""
}

func newOpenAIModelsMock() *openAIModelsMock {
	mock := &openAIModelsMock{}
	mock.server = httptest.NewServer(http.HandlerFunc(mock.handle))

	return mock
}

func (m *openAIModelsMock) handle(
	writer http.ResponseWriter,
	request *http.Request,
) {
	switch request.URL.Path {
	case providerModelsPath, providerModelsPathV1:
		m.handleModels(writer)
	case providerCompletionsPath, providerCompletionsPathV1:
		m.handleCompletion(writer, request)
	default:
		http.NotFound(writer, request)
	}
}

func (m *openAIModelsMock) handleModels(writer http.ResponseWriter) {
	m.modelsListed.Store(true)
	writer.Header().Set("Content-Type", jsonMediaType)

	if err := json.NewEncoder(writer).Encode(map[string]any{
		providerObjectField: providerModelsObjectType,
		"data": []map[string]any{{
			"id":                providerModel,
			providerObjectField: providerModelObjectType,
		}},
	}); err != nil {
		http.Error(
			writer,
			"failed to encode mock model response",
			http.StatusInternalServerError,
		)
	}
}

func (m *openAIModelsMock) handleCompletion(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if request.Method != http.MethodPost {
		http.Error(
			writer,
			"method not allowed",
			http.StatusMethodNotAllowed,
		)

		return
	}

	completionRequest, err := decodeCompletionRequest(request)
	if err != nil {
		http.Error(
			writer,
			"invalid completion request",
			http.StatusBadRequest,
		)

		return
	}

	if completionRequest.Model != providerModel || !completionRequest.Stream {
		http.Error(
			writer,
			"unexpected completion request",
			http.StatusBadRequest,
		)

		return
	}

	m.recordSystemPrompt(completionRequest.systemPrompt())

	if !m.awaitCompletionRelease(request.Context()) {
		return
	}

	script := m.currentScript()
	if script == nil {
		m.writeCompletion(writer)

		return
	}

	m.writeScriptedCompletion(
		writer,
		script,
		completionRequest.toolMessageCount(),
	)
}

func (m *openAIModelsMock) recordSystemPrompt(prompt string) {
	m.promptMu.Lock()
	defer m.promptMu.Unlock()

	m.lastSystemPrompt = prompt
}

func (m *openAIModelsMock) systemPrompt() string {
	m.promptMu.RLock()
	defer m.promptMu.RUnlock()

	return m.lastSystemPrompt
}

func (m *openAIModelsMock) writeScriptedCompletion(
	writer http.ResponseWriter,
	script *ScriptedToolTurn,
	round int,
) {
	stream, err := scriptedCompletionStream(round, script)
	if err != nil {
		http.Error(
			writer,
			"failed to encode mock scripted completion response",
			http.StatusInternalServerError,
		)

		return
	}

	writer.Header().Set("Content-Type", eventStreamMediaType)

	if _, err := writer.Write(stream); err != nil {
		return
	}
}

// scriptedCompletionStream picks the scripted turn's next move purely from
// how many tool-role messages the incoming transcript already carries.
func scriptedCompletionStream(
	round int,
	script *ScriptedToolTurn,
) ([]byte, error) {
	switch round {
	case scriptedRoundReadFile:
		return openAIToolCallStream(
			ScriptedCallIDReadFile,
			scriptedToolNameReadFile,
			script.ReadFileArguments,
		)
	case scriptedRoundEditFile:
		if script.ApplyPatchArguments != nil {
			return openAIToolCallStream(
				ScriptedCallIDApplyPatch,
				scriptedToolNameApplyPatch,
				script.ApplyPatchArguments,
			)
		}

		return openAIToolCallStream(
			ScriptedCallIDEditFile,
			scriptedToolNameEditFile,
			script.EditFileArguments,
		)
	case scriptedRoundRunCommand:
		return openAIToolCallStream(
			ScriptedCallIDRunCommand,
			scriptedToolNameRunCommand,
			script.RunCommandArguments,
		)
	default:
		return openAITextCompletionStream(script.FinalAnswer)
	}
}

func decodeCompletionRequest(
	request *http.Request,
) (providerCompletionRequest, error) {
	completionRequest := providerCompletionRequest{}

	decoder := json.NewDecoder(request.Body)

	if err := decoder.Decode(&completionRequest); err != nil {
		return providerCompletionRequest{}, ctxerrors.Wrap(
			err,
			"decode completion request",
		)
	}

	return completionRequest, nil
}

func (m *openAIModelsMock) awaitCompletionRelease(ctx context.Context) bool {
	hold := m.takeNextCompletionHold()
	if hold == nil {
		return true
	}

	close(hold.observed)

	select {
	case <-ctx.Done():
		return false
	case <-hold.release:
		return true
	}
}

func (m *openAIModelsMock) writeCompletion(writer http.ResponseWriter) {
	stream, err := openAICompletionStream(m.completionCount.Add(1))
	if err != nil {
		http.Error(
			writer,
			"failed to encode mock completion response",
			http.StatusInternalServerError,
		)

		return
	}

	writer.Header().Set("Content-Type", eventStreamMediaType)

	if _, err := writer.Write(stream); err != nil {
		return
	}
}

func (m *openAIModelsMock) HoldNextCompletion() (*CompletionHold, error) {
	m.holdMu.Lock()
	defer m.holdMu.Unlock()

	if m.nextHold != nil {
		return nil, ctxerrors.New("a provider completion is already held")
	}

	hold := &completionHold{
		observed: make(chan struct{}),
		release:  make(chan struct{}),
	}
	m.nextHold = hold

	return &CompletionHold{
		Observed: hold.observed,
		release: func() {
			hold.releaseOnce.Do(func() {
				close(hold.release)
			})
		},
	}, nil
}

func (m *openAIModelsMock) takeNextCompletionHold() *completionHold {
	m.holdMu.Lock()
	defer m.holdMu.Unlock()

	hold := m.nextHold
	m.nextHold = nil

	return hold
}

func openAICompletionStream(sequence int64) ([]byte, error) {
	text := providerResponsePrefix + strconv.FormatInt(sequence, 10)

	return openAIReasoningCompletionStream(DefaultProviderReasoning, text)
}

// openAIReasoningCompletionStream renders visible reasoning followed by text,
// using OpenAI-compatible reasoning_content wire data.
func openAIReasoningCompletionStream(
	reasoning string,
	text string,
) ([]byte, error) {
	chunks := []map[string]any{
		openAIChunkEnvelope(map[string]any{
			openAIFieldIndex: 0,
			openAIFieldDelta: map[string]any{
				openAIDeltaFieldRole:        providerRoleAssistant,
				openAIDeltaReasoningContent: reasoning,
			},
			openAIFieldFinishReason: nil,
		}),
		openAIChunkEnvelope(map[string]any{
			openAIFieldIndex: 0,
			openAIFieldDelta: map[string]any{
				"content": text,
			},
			openAIFieldFinishReason: nil,
		}),
		openAIChunkEnvelope(map[string]any{
			openAIFieldIndex:        0,
			openAIFieldDelta:        map[string]any{},
			openAIFieldFinishReason: openAIFinishReasonStop,
		}),
	}

	return encodeOpenAIStream(chunks)
}

// openAITextCompletionStream renders a plain two-chunk assistant text
// completion: one chunk carrying the full text, one closing chunk with
// finish_reason stop.
func openAITextCompletionStream(text string) ([]byte, error) {
	chunks := []map[string]any{
		openAIChunkEnvelope(map[string]any{
			openAIFieldIndex: 0,
			openAIFieldDelta: map[string]any{
				openAIDeltaFieldRole: providerRoleAssistant,
				"content":            text,
			},
			openAIFieldFinishReason: nil,
		}),
		openAIChunkEnvelope(map[string]any{
			openAIFieldIndex:        0,
			openAIFieldDelta:        map[string]any{},
			openAIFieldFinishReason: openAIFinishReasonStop,
		}),
	}

	return encodeOpenAIStream(chunks)
}

// openAIToolCallStream renders a two-chunk tool-call completion: one chunk
// carrying the full function name and arguments at tool_calls[0], one
// closing chunk with finish_reason tool_calls. A real provider may split
// the arguments across several deltas; the engine accumulates by index
// either way, so one full delta is a faithful, simpler stand-in.
func openAIToolCallStream(
	callID string,
	name string,
	arguments map[string]any,
) ([]byte, error) {
	argumentsJSON, err := json.Marshal(arguments)
	if err != nil {
		return nil, ctxerrors.Wrap(
			err,
			"marshal scripted tool call arguments",
		)
	}

	chunks := []map[string]any{
		openAIChunkEnvelope(map[string]any{
			openAIFieldIndex: 0,
			openAIFieldDelta: map[string]any{
				openAIDeltaFieldRole: providerRoleAssistant,
				openAIDeltaFieldToolCalls: []map[string]any{{
					openAIFieldIndex: 0,
					"id":             callID,
					"type":           openAIToolCallType,
					"function": map[string]any{
						"name":      name,
						"arguments": string(argumentsJSON),
					},
				}},
			},
			openAIFieldFinishReason: nil,
		}),
		openAIChunkEnvelope(map[string]any{
			openAIFieldIndex:        0,
			openAIFieldDelta:        map[string]any{},
			openAIFieldFinishReason: openAIFinishReasonToolCalls,
		}),
	}

	return encodeOpenAIStream(chunks)
}

// openAIChunkEnvelope wraps one choice in the chunk header every mock
// completion event shares.
func openAIChunkEnvelope(choice map[string]any) map[string]any {
	return map[string]any{
		"id":                providerCompletionID,
		providerObjectField: providerChunkObjectType,
		"created":           providerCreatedAt,
		"model":             providerModel,
		"choices":           []map[string]any{choice},
	}
}

// encodeOpenAIStream renders chunks as SSE data events terminated by the
// OpenAI [DONE] sentinel.
func encodeOpenAIStream(chunks []map[string]any) ([]byte, error) {
	stream := make([]byte, 0, len(openAIDoneEvent))

	for _, chunk := range chunks {
		var err error

		stream, err = appendOpenAIStreamEvent(stream, chunk)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "encode mock OpenAI stream event")
		}
	}

	return append(stream, openAIDoneEvent...), nil
}

func appendOpenAIStreamEvent(
	stream []byte,
	payload map[string]any,
) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "marshal mock OpenAI stream event")
	}

	stream = append(stream, openAIDataPrefix...)
	stream = append(stream, encoded...)

	return append(stream, '\n', '\n'), nil
}

func appEnvironment(
	providerBaseURL string,
	listenAddress string,
	metricsListenAddress string,
) (map[string]string, error) {
	upstreams, err := json.Marshal([]map[string]string{{
		"name":     providerName,
		"provider": providerType,
		"baseUrl":  providerBaseURL,
	}})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "marshal test provider configuration")
	}

	return map[string]string{
		"PEEN_CONFIG_DIR":             appConfigDirectory,
		"PEEN_WORKING_DIR":            appWorkingDirectory,
		"PEEN_AGENT":                  appAgentName,
		"PEEN_HTTP_LISTEN_ADDRESS":    listenAddress,
		"PEEN_METRICS_LISTEN_ADDRESS": metricsListenAddress,
		"PEEN_UPSTREAMS":              string(upstreams),
		"PEEN_DEFAULT_MODEL":          providerName + "/" + providerModel,
		"PEEN_API_TOKEN":              TestAPIToken,
	}, nil
}

func reserveLoopbackAddress(ctx context.Context) (string, error) {
	listenerConfig := net.ListenConfig{}

	listener, err := listenerConfig.Listen(
		ctx,
		appNetwork,
		appLoopbackAddress,
	)
	if err != nil {
		return "", ctxerrors.Wrap(err, "reserve loopback API address")
	}

	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return "", ctxerrors.Wrap(err, "release loopback API address")
	}

	return address, nil
}

// repoRoot walks up from the test's working directory to the module root (the
// directory holding go.mod), which is where the Dockerfile and its build
// context live.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", ctxerrors.Wrap(err, "get working directory")
	}

	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errNoGoMod
		}

		dir = parent
	}
}
