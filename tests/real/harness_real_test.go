//go:build real

package realtest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	"github.com/psyb0t/peen/internal/pkg/harness"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	realHarnessTestTimeout        = 8 * time.Minute
	realHarnessStartupTimeout     = 30 * time.Second
	realHarnessShutdownTimeout    = 15 * time.Second
	realHarnessPollInterval       = 50 * time.Millisecond
	realHarnessDirectoryMode      = 0o700
	realHarnessFileMode           = 0o600
	realHarnessBinaryName         = "peen-real-harness"
	realHarnessCommandPackage     = "./cmd"
	realHarnessRunCommand         = "run"
	realHarnessNetwork            = "tcp"
	realHarnessLoopback           = "127.0.0.1:0"
	realHarnessAPIPath            = "/v1"
	realHarnessMessagesPath       = realHarnessAPIPath + "/messages"
	realHarnessWebSocketPath      = realHarnessAPIPath + "/ws"
	realHarnessSessionPath        = realHarnessAPIPath + "/session"
	realHarnessEventsPath         = realHarnessSessionPath + "/events"
	realHarnessAgentRunsPath      = realHarnessSessionPath + "/agents"
	realHarnessJobsPath           = realHarnessSessionPath + "/jobs"
	realHarnessReadyPath          = "/ready"
	realHarnessAuthorization      = "Authorization"
	realHarnessSessionIDHeader    = "X-Session-ID"
	realHarnessContentType        = "Content-Type"
	realHarnessBearerPrefix       = "Bearer "
	realHarnessJSONMediaType      = "application/json"
	realHarnessWebSocketSessionID = "sessionId"
	realHarnessWebSocketProtocol  = "peen.v1"
	realHarnessWebSocketBearer    = "peen.bearer."
	realHarnessMessageSendEvent   = "message.send"
	realHarnessMessageCompleted   = "message.completed"
	realHarnessMessageFailed      = "message.failed"
	realHarnessRootAgent          = "default"
	realHarnessSkill              = "fixture-service"
	realHarnessReviewer           = "fixture-reviewer"
	realHarnessPatchEvent         = "fixture.patch.applied"
	realHarnessWakeEvent          = "fixture.wake"
	realHarnessFixtureTestCommand = "go test ./..."
	realHarnessPatchOnlyReason    = "Fixture changes must use apply_patch."
	realHarnessCommandOnlyReason  = "Only the fixture test command is allowed."
	realHarnessHookHelperEnv      = "PEEN_REAL_HOOK_HELPER"
	realHarnessHookHelperEnabled  = "enabled"
	realHarnessReadMarker         = "read-observed"
	realHarnessTaskMessage        = `This is an execution task with mandatory tool-call acceptance criteria, not a request for advice. Do not return a final answer until all criteria below have completed successfully. In this exact order: (1) call use_skill with fixture-service; (2) call launch_agent with fixture-reviewer, asking it to inspect internal/status/status.go and internal/status/status_test.go without changes; (3) after the child returns, call read_file yourself for each of those two paths; (4) call apply_patch, and only apply_patch, to make Label return "ready" and make the test expect "ready"; (5) call run_command with exactly "go test ./..." from this workspace. Do not alter any other path.`

	realHarnessConfigRuleMarker    = "REAL_HARNESS_CONFIG_RULE_MARKER"
	realHarnessWorkspaceRuleMarker = "REAL_HARNESS_WORKSPACE_RULE_MARKER"
	realHarnessProjectsRuleMarker  = "REAL_HARNESS_PROJECTS_RULE_MARKER"
	realHarnessCatalogRuleMarker   = "REAL_HARNESS_CATALOG_RULE_MARKER"
	realHarnessServiceRuleMarker   = "REAL_HARNESS_SERVICE_RULE_MARKER"
	realHarnessSkillBodyMarker     = "REAL_HARNESS_SKILL_FULL_BODY_MARKER"
	realHarnessReviewerMarker      = "REAL_HARNESS_REVIEWER_FULL_BODY_MARKER"
	realHarnessWakeHandlerMarker   = "REAL_HARNESS_WAKE_HANDLER_FULL_BODY_MARKER"
	realHarnessRootAgentMarker     = "REAL_HARNESS_ROOT_AGENT_MARKER"

	realHarnessTrustedRuntimeMarker = "Trusted runtime context:"
	realHarnessOperatingSystemLead  = "Operating system: "
	realHarnessArchitectureLead     = "Architecture: "
	realHarnessTimezoneLead         = "Local timezone: "
)

type realHarnessFixture struct {
	configDirectory string
	workspace       string
	service         string
	implementation  string
	testFile        string
	auditDirectory  string
}

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

type realHarnessPromptRecord struct {
	markers map[string]bool
	roles   map[string]int
}

type realHarnessWebSocketMessage struct {
	Message   string `json:"message"`
	Workspace string `json:"workspace"`
}

type realHarnessWebSocketMessageResult struct {
	Queued bool `json:"queued"`
}

type realHarnessProviderProxy struct {
	target  *url.URL
	client  *http.Client
	markers []string

	mutex   sync.Mutex
	prompts []realHarnessPromptRecord
	server  *httptest.Server
}

func TestRealHarnessExecutesTheFullCodingLifecycle(t *testing.T) {
	configured := realConfig(t)
	upstreams, err := configured.Upstreams()
	require.NoError(t, err)

	providerName, modelID, found := strings.Cut(configured.DefaultModel, "/")
	require.True(t, found)
	require.NotEmpty(t, providerName)
	require.NotEmpty(t, modelID)

	selectedUpstream, found := upstreamByName(upstreams, providerName)
	require.True(t, found)
	require.NotEmpty(t, selectedUpstream.BaseURL)

	fixture := newRealHarnessFixture(t)
	proxy := newRealHarnessProviderProxy(
		t,
		selectedUpstream.BaseURL,
		fixture.service,
	)
	t.Cleanup(proxy.Close)

	for index := range upstreams {
		if upstreams[index].Name == selectedUpstream.Name {
			upstreams[index].BaseURL = proxy.URL()
		}
	}

	encodedUpstreams, err := json.Marshal(upstreams)
	require.NoError(t, err)

	binary := buildRealHarnessBinary(t)
	process := startRealHarnessPeen(t, binary, fixture, string(encodedUpstreams), configured.DefaultModel)
	t.Cleanup(func() { process.stop(t) })

	sessionID := sendRealHarnessMessage(t, process, fixture.service)
	messages := listRealHarnessMessages(t, process.baseURL, process.apiToken, sessionID)
	t.Logf("real harness tool calls: %v", realHarnessToolCallNames(messages))
	assertRealHarnessTranscriptMessages(t, messages)
	assertRealHarnessTranscript(t, process.baseURL, process.apiToken, sessionID)
	assertRealHarnessAgentRun(t, process.baseURL, process.apiToken, sessionID)
	assertRealHarnessJob(t, process.baseURL, process.apiToken, sessionID, fixture.service)
	assertRealHarnessQueuedEvent(t, process.baseURL, process.apiToken, sessionID)
	assertRealHarnessFiles(t, fixture)

	publishRealHarnessWakeEvent(t, process.baseURL, process.apiToken, sessionID)
	waitForRealHarnessWake(t, process.baseURL, process.apiToken, sessionID)

	assertRealHarnessProviderContext(t, proxy, fixture.service)
	assertRealHarnessAuditLog(t, fixture.auditDirectory)
}

func TestRealHarnessFixtureResolvesAllInstructionLayers(t *testing.T) {
	fixture := newRealHarnessFixture(t)
	resolver, err := harness.NewResolver(
		fixture.configDirectory,
		harness.DefaultLimits(),
	)
	require.NoError(t, err)

	snapshot, err := resolver.Resolve(fixture.service)
	require.NoError(t, err)
	blocks, err := snapshot.PromptBlocks(realHarnessRootAgent)
	require.NoError(t, err)

	contents := make([]string, 0, len(blocks))
	for _, block := range blocks {
		contents = append(contents, block.Content)
	}

	prompt := strings.Join(contents, "\n")
	for _, marker := range []string{
		realHarnessConfigRuleMarker,
		realHarnessWorkspaceRuleMarker,
		realHarnessProjectsRuleMarker,
		realHarnessCatalogRuleMarker,
		realHarnessServiceRuleMarker,
	} {
		assert.Contains(t, prompt, marker)
	}
}

func TestRealHarnessHookHelper(_ *testing.T) {
	if os.Getenv(realHarnessHookHelperEnv) != realHarnessHookHelperEnabled {
		return
	}

	invocation := struct {
		Event          string          `json:"event"`
		Input          json.RawMessage `json:"input"`
		StateDirectory string          `json:"stateDirectory"`
	}{}
	if err := json.NewDecoder(os.Stdin).Decode(&invocation); err != nil {
		os.Exit(1)
	}

	if invocation.StateDirectory == "" {
		os.Exit(1)
	}

	marker := filepath.Join(invocation.StateDirectory, realHarnessReadMarker)
	switch invocation.Event {
	case "post_read_file":
		if err := os.WriteFile(marker, []byte("observed"), realHarnessFileMode); err != nil {
			os.Exit(1)
		}
	case "pre_apply_patch":
		if _, err := os.Stat(marker); err != nil {
			if _, writeErr := fmt.Fprint(
				os.Stdout,
				`{"decision":"deny","reason":"Read the target before applying a patch."}`,
			); writeErr != nil {
				os.Exit(1)
			}
			os.Exit(0)
		}

		if _, err := fmt.Fprint(os.Stdout, `{"decision":"allow"}`); err != nil {
			os.Exit(1)
		}
	case "pre_tool_use":
		input := struct {
			Command string `json:"command"`
		}{}
		if err := json.Unmarshal(invocation.Input, &input); err != nil {
			os.Exit(1)
		}

		if input.Command != realHarnessFixtureTestCommand {
			if _, err := fmt.Fprintf(
				os.Stdout,
				`{"decision":"deny","reason":%q}`,
				realHarnessCommandOnlyReason,
			); err != nil {
				os.Exit(1)
			}
			os.Exit(0)
		}

		if _, err := fmt.Fprint(os.Stdout, `{"decision":"allow"}`); err != nil {
			os.Exit(1)
		}
	default:
		os.Exit(1)
	}
}

func newRealHarnessFixture(t *testing.T) realHarnessFixture {
	t.Helper()

	root := t.TempDir()
	configDirectory := filepath.Join(root, "config")
	workspace := filepath.Join(root, "workspace")
	service := filepath.Join(workspace, "projects", "catalog", "service")
	auditDirectory := filepath.Join(root, "audit")
	implementation := filepath.Join(service, "internal", "status", "status.go")
	testFile := filepath.Join(service, "internal", "status", "status_test.go")

	writeRealHarnessFile(t, filepath.Join(configDirectory, "AGENTS.md"), realHarnessConfigRuleMarker+"\n")
	writeRealHarnessFile(t, filepath.Join(workspace, "AGENTS.md"), realHarnessWorkspaceRuleMarker+"\n")
	writeRealHarnessFile(t, filepath.Join(workspace, "projects", "AGENTS.md"), realHarnessProjectsRuleMarker+"\n")
	writeRealHarnessFile(t, filepath.Join(workspace, "projects", "catalog", "AGENTS.md"), realHarnessCatalogRuleMarker+"\n")
	writeRealHarnessFile(t, filepath.Join(service, "AGENTS.md"), realHarnessServiceRuleMarker+"\n")

	writeRealHarnessFile(
		t,
		filepath.Join(configDirectory, ".agents", "agents", "default.md"),
		realHarnessAgentDocument(
			realHarnessRootAgent,
			"Root agent for the live coding fixture. "+realHarnessRootAgentMarker,
			"",
			"Complete execution requests with the supplied local tools. Do not return a final response until every explicit acceptance criterion has a matching tool result.",
		),
	)
	writeRealHarnessFile(
		t,
		filepath.Join(service, ".agents", "agents", realHarnessReviewer+".md"),
		realHarnessAgentDocument(
			realHarnessReviewer,
			"Read-only reviewer for the live coding fixture.",
			"read_file,list_files,search_text",
			realHarnessReviewerMarker,
		),
	)

	writeRealHarnessSkill(t, configDirectory, "config-observer")
	writeRealHarnessSkill(t, workspace, "workspace-observer")
	writeRealHarnessSkill(t, filepath.Join(workspace, "projects"), "projects-observer")
	writeRealHarnessSkill(t, filepath.Join(workspace, "projects", "catalog"), "catalog-observer")
	writeRealHarnessFile(
		t,
		filepath.Join(service, ".agents", "skills", realHarnessSkill, "SKILL.md"),
		"---\nname: fixture-service\ndescription: Safely complete the live coding fixture.\n---\n"+
			realHarnessSkillBodyMarker+"\nUse apply_patch for mutations. Read every target before patching it. Run the fixture test after the patch.\n",
	)

	writeRealHarnessFile(
		t,
		filepath.Join(service, ".agents", "events", realHarnessWakeEvent+".md"),
		"---\ntype: fixture.wake\nagent: fixture-reviewer\ndelivery: wake\n---\n"+
			realHarnessWakeHandlerMarker+"\nReview the queued fixture event and report that it was handled.\n",
	)
	writeRealHarnessFile(
		t,
		filepath.Join(configDirectory, ".agents", "hooks.yaml"),
		realHarnessConfigHooksDocument(),
	)
	writeRealHarnessFile(
		t,
		filepath.Join(workspace, ".agents", "hooks.yaml"),
		realHarnessInjectingHookDocument("workspace-patch-policy", "workspace-policy-injection"),
	)
	writeRealHarnessFile(
		t,
		filepath.Join(workspace, "projects", "catalog", ".agents", "hooks.yaml"),
		realHarnessInjectingHookDocument("catalog-patch-policy", "catalog-policy-injection"),
	)
	writeRealHarnessFile(
		t,
		filepath.Join(service, ".agents", "hooks.yaml"),
		realHarnessServiceHooksDocument(),
	)

	writeRealHarnessFile(t, filepath.Join(service, "go.mod"), "module fixture.local/service\n\ngo 1.26.6\n")
	writeRealHarnessFile(t, implementation, "package status\n\nfunc Label() string {\n\treturn \"pending\"\n}\n")
	writeRealHarnessFile(t, testFile, "package status\n\nimport \"testing\"\n\nfunc TestLabel(t *testing.T) {\n\tif got := Label(); got != \"pending\" {\n\t\tt.Fatalf(\"Label() = %q, want pending\", got)\n\t}\n}\n")

	return realHarnessFixture{
		configDirectory: configDirectory,
		workspace:       workspace,
		service:         service,
		implementation:  implementation,
		testFile:        testFile,
		auditDirectory:  auditDirectory,
	}
}

func writeRealHarnessSkill(t *testing.T, directory, name string) {
	t.Helper()

	writeRealHarnessFile(
		t,
		filepath.Join(directory, ".agents", "skills", name, "SKILL.md"),
		"---\nname: "+name+"\ndescription: Metadata fixture skill.\n---\n"+
			"This skill proves that the "+name+" layer was resolved.\n",
	)
}

func realHarnessAgentDocument(name, description, allowedTools, instructions string) string {
	allowedToolsLine := ""
	if allowedTools != "" {
		allowedToolsLine = "allowed-tools: " + allowedTools + "\n"
	}

	return "---\nname: " + name + "\ndescription: " + description + "\n" +
		allowedToolsLine + "---\n" + instructions + "\n"
}

func realHarnessConfigHooksDocument() string {
	return fmt.Sprintf(`version: 1
post_read_file:
  - name: config-remember-read
    match:
      path: internal/status/status.go
    actions:
      - name: record-read-state
        type: command
        command: %q
        args: ["-test.run=^TestRealHarnessHookHelper$"]
        environment:
          PEEN_REAL_HOOK_HELPER: enabled
pre_apply_patch:
  - name: config-require-read
    actions:
      - name: allow-only-after-read
        type: command
        command: %q
        args: ["-test.run=^TestRealHarnessHookHelper$"]
        environment:
          PEEN_REAL_HOOK_HELPER: enabled
pre_tool_use:
  - name: config-allow-only-fixture-test
    match:
      tool: run_command
    actions:
      - name: deny-unapproved-command
        type: command
        command: %q
        args: ["-test.run=^TestRealHarnessHookHelper$"]
        environment:
          PEEN_REAL_HOOK_HELPER: enabled
`, os.Args[0], os.Args[0], os.Args[0])
}

func realHarnessInjectingHookDocument(name, message string) string {
	return "version: 1\npre_apply_patch:\n  - name: " + name +
		"\n    actions:\n      - name: " + name +
		"-action\n        type: inject\n        message: " + message + "\n"
}

func realHarnessServiceHooksDocument() string {
	return fmt.Sprintf(`version: 1
pre_write_file:
  - name: service-require-patch-for-writes
    actions:
      - name: deny-direct-write
        type: deny
        reason: %q
pre_edit_file:
  - name: service-require-patch-for-edits
    actions:
      - name: deny-direct-edit
        type: deny
        reason: %q
post_apply_patch:
  - name: service-patch-event
    actions:
      - name: emit-patch-event
        type: emit_event
        event_type: fixture.patch.applied
        summary: Fixture patch was applied.
        delivery: queue
`, realHarnessPatchOnlyReason, realHarnessPatchOnlyReason)
}

func writeRealHarnessFile(t *testing.T, filePath, content string) {
	t.Helper()

	_, err := os.Lstat(filePath)
	require.ErrorIs(t, err, fs.ErrNotExist)
	require.NoError(t, os.MkdirAll(filepath.Dir(filePath), realHarnessDirectoryMode))
	require.NoError(t, os.WriteFile(filePath, []byte(content), realHarnessFileMode))
}

func buildRealHarnessBinary(t *testing.T) string {
	t.Helper()

	repository, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)

	binary := filepath.Join(t.TempDir(), realHarnessBinaryName)
	command := exec.Command("go", "build", "-o", binary, realHarnessCommandPackage)
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

	apiAddress := reserveRealHarnessAddress(t)
	metricsAddress := reserveRealHarnessAddress(t)
	apiToken := uuid.NewString()
	process := &runningRealHarnessPeen{
		baseURL:  "http://" + apiAddress,
		apiToken: apiToken,
		done:     make(chan error, 1),
	}
	process.command = exec.Command(binary, realHarnessRunCommand)
	process.command.Env = realHarnessEnvironment(
		fixture,
		apiAddress,
		metricsAddress,
		apiToken,
		upstreams,
		defaultModel,
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
) []string {
	environment := make(map[string]string, len(os.Environ())+16)
	for _, value := range os.Environ() {
		name, _, found := strings.Cut(value, "=")
		if found {
			environment[name] = value
		}
	}

	for name, value := range map[string]string{
		"PEEN_CONFIG_DIR":             fixture.configDirectory,
		"PEEN_WORKING_DIR":            fixture.workspace,
		"PEEN_AGENT":                  realHarnessRootAgent,
		"PEEN_HTTP_LISTEN_ADDRESS":    apiAddress,
		"PEEN_METRICS_LISTEN_ADDRESS": metricsAddress,
		"PEEN_UPSTREAMS":              upstreams,
		"PEEN_DEFAULT_MODEL":          defaultModel,
		"PEEN_API_TOKEN":              apiToken,
		"PEEN_ENABLE_WORKSPACE_HOOKS": "true",
		"PEEN_TURN_TIMEOUT":           realHarnessTestTimeout.String(),
		"PEEN_LOG_DIRECTORY":          fixture.auditDirectory,
		"PEEN_LOG_RETENTION_DAYS":     "14",
		"LOG_LEVEL":                   "debug",
	} {
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

func awaitRealHarnessReady(t *testing.T, process *runningRealHarnessPeen) {
	t.Helper()

	deadline := time.Now().Add(realHarnessStartupTimeout)
	client := &http.Client{Timeout: realHarnessPollInterval}
	for time.Now().Before(deadline) {
		response, err := client.Get(process.baseURL + realHarnessReadyPath)
		if err == nil {
			closeErr := response.Body.Close()
			if response.StatusCode == http.StatusOK && closeErr == nil {
				return
			}
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

func sendRealHarnessMessage(
	t *testing.T,
	process *runningRealHarnessPeen,
	workspace string,
) uuid.UUID {
	t.Helper()

	sessionID := uuid.New()
	connection := dialRealHarnessWebSocket(t, process, sessionID)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	require.NoError(t, connection.WriteJSON(dabluveees.NewEvent(
		realHarnessMessageSendEvent,
		realHarnessWebSocketMessage{
			Message:   realHarnessTaskMessage,
			Workspace: workspace,
		},
	)))
	awaitRealHarnessWebSocketCompletion(t, process, connection)

	return sessionID
}

func dialRealHarnessWebSocket(
	t *testing.T,
	process *runningRealHarnessPeen,
	sessionID uuid.UUID,
) *websocket.Conn {
	t.Helper()

	endpoint, err := url.Parse(process.baseURL)
	require.NoError(t, err)
	endpoint.Scheme = "ws"
	endpoint.Path = realHarnessWebSocketPath
	query := endpoint.Query()
	query.Set(realHarnessWebSocketSessionID, sessionID.String())
	endpoint.RawQuery = query.Encode()

	dialer := websocket.Dialer{
		HandshakeTimeout: realHarnessStartupTimeout,
		Subprotocols: []string{
			realHarnessWebSocketProtocol,
			realHarnessWebSocketBearer + base64.RawURLEncoding.EncodeToString(
				[]byte(process.apiToken),
			),
		},
	}
	connection, response, err := dialer.Dial(endpoint.String(), nil)
	if response != nil {
		t.Cleanup(func() { require.NoError(t, response.Body.Close()) })
	}
	require.NoErrorf(t, err, "Peen output:\n%s", process.output.String())
	require.Equal(t, realHarnessWebSocketProtocol, connection.Subprotocol())

	return connection
}

func awaitRealHarnessWebSocketCompletion(
	t *testing.T,
	process *runningRealHarnessPeen,
	connection *websocket.Conn,
) {
	t.Helper()

	require.NoError(t, connection.SetReadDeadline(
		time.Now().Add(realHarnessTestTimeout),
	))
	for {
		event := dabluveees.Event{}
		require.NoErrorf(
			t,
			connection.ReadJSON(&event),
			"Peen output:\n%s",
			process.output.String(),
		)

		switch event.Type {
		case realHarnessMessageCompleted:
			result := realHarnessWebSocketMessageResult{}
			require.NoError(t, json.Unmarshal(event.Data, &result))
			require.False(t, result.Queued)

			return
		case realHarnessMessageFailed:
			require.Failf(
				t,
				"WebSocket message failed",
				"event=%s Peen output:\n%s",
				event.Data,
				process.output.String(),
			)
		}
	}
}

func assertRealHarnessFiles(t *testing.T, fixture realHarnessFixture) {
	t.Helper()

	implementation, err := os.ReadFile(fixture.implementation)
	require.NoError(t, err)
	assert.Contains(t, string(implementation), "return \"ready\"")

	testContent, err := os.ReadFile(fixture.testFile)
	require.NoError(t, err)
	assert.Contains(t, string(testContent), "want ready")
	assert.Contains(t, string(testContent), "!= \"ready\"")
}

func assertRealHarnessTranscript(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
) {
	t.Helper()

	messages := listRealHarnessMessages(t, baseURL, apiToken, sessionID)
	assertRealHarnessTranscriptMessages(t, messages)
}

func assertRealHarnessTranscriptMessages(t *testing.T, messages []api.Message) {
	t.Helper()

	names := realHarnessToolCallNames(messages)
	assertRealHarnessToolSubsequence(
		t,
		names,
		[]string{"use_skill", "launch_agent", "read_file", "apply_patch", "run_command"},
	)
	assertRealHarnessDirectFileToolsWereDenied(t, messages)
}

func assertRealHarnessDirectFileToolsWereDenied(t *testing.T, messages []api.Message) {
	t.Helper()

	toolNames := make(map[string]string)
	for _, message := range messages {
		if message.ToolCalls == nil {
			continue
		}

		for _, toolCall := range *message.ToolCalls {
			toolNames[toolCall.Id] = toolCall.Name
		}
	}

	for _, message := range messages {
		if message.ToolCallId == nil {
			continue
		}

		toolName := toolNames[*message.ToolCallId]
		if toolName != "write_file" && toolName != "edit_file" {
			continue
		}

		require.NotNil(t, message.IsError)
		assert.True(t, *message.IsError)
		assert.Contains(t, message.Content, realHarnessPatchOnlyReason)
	}
}

func listRealHarnessMessages(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
) []api.Message {
	t.Helper()

	request, err := http.NewRequest(
		http.MethodGet,
		baseURL+realHarnessMessagesPath+"?limit=200&order=asc",
		nil,
	)
	require.NoError(t, err)
	setRealHarnessSessionHeaders(request, apiToken, sessionID)

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusOK, response.StatusCode)

	page := api.MessagePage{}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&page))
	require.False(t, page.HasMore)

	return page.Items
}

func realHarnessToolCallNames(messages []api.Message) []string {
	names := make([]string, 0)
	for _, message := range messages {
		if message.ToolCalls == nil {
			continue
		}

		for _, toolCall := range *message.ToolCalls {
			names = append(names, toolCall.Name)
		}
	}

	return names
}

func assertRealHarnessToolSubsequence(t *testing.T, names, wanted []string) {
	t.Helper()

	position := 0
	for _, name := range names {
		if position < len(wanted) && name == wanted[position] {
			position++
		}
	}

	assert.Equal(t, len(wanted), position, "tool sequence: %v", names)
}

func assertRealHarnessAgentRun(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
) {
	t.Helper()

	page := getRealHarnessJSON[api.AgentRunPage](
		t,
		baseURL+realHarnessAgentRunsPath+"?limit=50",
		apiToken,
		sessionID,
	)
	require.NotEmpty(t, page.Agents)
	assert.Equal(t, realHarnessReviewer, page.Agents[0].Name)
	assert.Equal(t, "stored", string(page.Agents[0].Definition))
	assert.Equal(t, "completed", string(page.Agents[0].State))
}

func assertRealHarnessJob(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
	service string,
) {
	t.Helper()

	page := getRealHarnessJSON[api.JobPage](
		t,
		baseURL+realHarnessJobsPath+"?limit=50",
		apiToken,
		sessionID,
	)
	require.NotEmpty(t, page.Jobs)

	found := false
	for _, job := range page.Jobs {
		assert.Equal(t, realHarnessFixtureTestCommand, job.Command)
		assert.Equal(t, service, job.Directory)
		assert.Equal(t, int32(0), job.ExitCode)
		found = true
	}

	assert.True(t, found)
}

func assertRealHarnessQueuedEvent(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
) {
	t.Helper()

	messages := listRealHarnessMessages(t, baseURL, apiToken, sessionID)
	for _, message := range messages {
		if strings.Contains(message.Content, realHarnessPatchEvent) {
			return
		}
	}

	require.Failf(t, "patch event was not delivered", "event type %q", realHarnessPatchEvent)
}

func publishRealHarnessWakeEvent(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
) {
	t.Helper()

	delivery := api.SessionEventRequestDelivery("wake")
	payload, err := json.Marshal(api.SessionEventRequest{
		Type:     realHarnessWakeEvent,
		Summary:  "Review the completed fixture.",
		Delivery: &delivery,
	})
	require.NoError(t, err)

	request, err := http.NewRequest(
		http.MethodPost,
		baseURL+realHarnessEventsPath,
		bytes.NewReader(payload),
	)
	require.NoError(t, err)
	request.Header.Set(realHarnessContentType, realHarnessJSONMediaType)
	setRealHarnessSessionHeaders(request, apiToken, sessionID)

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusAccepted, response.StatusCode)
}

func waitForRealHarnessWake(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
) {
	t.Helper()

	deadline := time.Now().Add(realHarnessTestTimeout)
	for time.Now().Before(deadline) {
		session := getRealHarnessJSON[api.Session](
			t,
			baseURL+realHarnessSessionPath,
			apiToken,
			sessionID,
		)
		if !session.ActiveTurn && session.CompletedTurnCount >= 2 {
			return
		}

		time.Sleep(realHarnessPollInterval)
	}

	require.Fail(t, "wake turn did not complete")
}

func getRealHarnessJSON[T any](
	t *testing.T,
	endpoint, apiToken string,
	sessionID uuid.UUID,
) T {
	t.Helper()

	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	require.NoError(t, err)
	setRealHarnessSessionHeaders(request, apiToken, sessionID)

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusOK, response.StatusCode)

	result := *new(T)
	require.NoError(t, json.NewDecoder(response.Body).Decode(&result))

	return result
}

func setRealHarnessSessionHeaders(
	request *http.Request,
	apiToken string,
	sessionID uuid.UUID,
) {
	request.Header.Set(realHarnessAuthorization, realHarnessBearerPrefix+apiToken)
	request.Header.Set(realHarnessSessionIDHeader, sessionID.String())
}

func newRealHarnessProviderProxy(
	t *testing.T,
	upstreamBaseURL, workspace string,
) *realHarnessProviderProxy {
	t.Helper()

	target, err := url.Parse(upstreamBaseURL)
	require.NoError(t, err)
	require.True(t, target.IsAbs())
	workspaceMarker, err := json.Marshal(workspace)
	require.NoError(t, err)

	proxy := &realHarnessProviderProxy{
		target: target,
		client: &http.Client{},
		markers: []string{
			realHarnessConfigRuleMarker,
			realHarnessWorkspaceRuleMarker,
			realHarnessProjectsRuleMarker,
			realHarnessCatalogRuleMarker,
			realHarnessServiceRuleMarker,
			realHarnessRootAgentMarker,
			realHarnessSkill,
			realHarnessReviewer,
			realHarnessSkillBodyMarker,
			realHarnessReviewerMarker,
			realHarnessPatchEvent,
			realHarnessWakeHandlerMarker,
			realHarnessTrustedRuntimeMarker,
			realHarnessOperatingSystemLead + runtime.GOOS,
			realHarnessArchitectureLead + runtime.GOARCH,
			realHarnessTimezoneLead + time.Now().Location().String(),
			string(workspaceMarker),
		},
	}
	proxy.server = httptest.NewServer(http.HandlerFunc(proxy.handle))

	return proxy
}

func (p *realHarnessProviderProxy) URL() string {
	return p.server.URL
}

func (p *realHarnessProviderProxy) Close() {
	p.server.Close()
}

func (p *realHarnessProviderProxy) handle(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(writer, "read provider request", http.StatusBadRequest)

		return
	}
	if closeErr := request.Body.Close(); closeErr != nil {
		http.Error(writer, "close provider request", http.StatusBadRequest)

		return
	}

	p.recordPrompt(body)

	forwardURL := *p.target
	forwardURL.Path = path.Join(p.target.Path, request.URL.Path)
	forwardURL.RawPath = ""
	forwardURL.RawQuery = request.URL.RawQuery
	forward, err := http.NewRequestWithContext(
		request.Context(),
		request.Method,
		forwardURL.String(),
		bytes.NewReader(body),
	)
	if err != nil {
		http.Error(writer, "build provider request", http.StatusInternalServerError)

		return
	}
	forward.Header = request.Header.Clone()

	response, err := p.client.Do(forward)
	if err != nil {
		http.Error(writer, "forward provider request", http.StatusBadGateway)

		return
	}
	defer response.Body.Close()

	for key, values := range response.Header {
		writer.Header()[key] = append([]string(nil), values...)
	}
	writer.WriteHeader(response.StatusCode)
	_, _ = io.Copy(writer, response.Body)
}

func (p *realHarnessProviderProxy) recordPrompt(body []byte) {
	payload := any(nil)
	if err := json.Unmarshal(body, &payload); err != nil || !realHarnessPromptPayload(payload) {
		return
	}

	fragments := realHarnessTextFragments(payload)
	record := realHarnessPromptRecord{
		markers: make(map[string]bool, len(p.markers)),
		roles:   realHarnessRoles(payload),
	}
	for _, marker := range p.markers {
		for _, fragment := range fragments {
			if strings.Contains(fragment, marker) {
				record.markers[marker] = true

				break
			}
		}
	}

	p.mutex.Lock()
	p.prompts = append(p.prompts, record)
	p.mutex.Unlock()
}

func realHarnessPromptPayload(payload any) bool {
	object, ok := payload.(map[string]any)
	if !ok {
		return false
	}

	_, hasMessages := object["messages"]
	_, hasSystem := object["system"]

	return hasMessages || hasSystem
}

func realHarnessTextFragments(value any) []string {
	fragments := make([]string, 0)
	var visit func(any)
	visit = func(item any) {
		switch found := item.(type) {
		case string:
			fragments = append(fragments, found)
		case []any:
			for _, child := range found {
				visit(child)
			}
		case map[string]any:
			for _, child := range found {
				visit(child)
			}
		}
	}
	visit(value)

	return fragments
}

func realHarnessRoles(payload any) map[string]int {
	roles := map[string]int{}
	object, ok := payload.(map[string]any)
	if !ok {
		return roles
	}

	messages, ok := object["messages"].([]any)
	if !ok {
		return roles
	}
	for _, value := range messages {
		message, ok := value.(map[string]any)
		if !ok {
			continue
		}

		role, ok := message["role"].(string)
		if ok {
			roles[role]++
		}
	}

	return roles
}

func (p *realHarnessProviderProxy) promptRecords() []realHarnessPromptRecord {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	return append([]realHarnessPromptRecord(nil), p.prompts...)
}

func assertRealHarnessProviderContext(
	t *testing.T,
	proxy *realHarnessProviderProxy,
	workspace string,
) {
	t.Helper()

	workspaceMarker, err := json.Marshal(workspace)
	require.NoError(t, err)
	records := proxy.promptRecords()
	require.NotEmpty(t, records)

	initial := records[0]
	for _, marker := range []string{
		realHarnessConfigRuleMarker,
		realHarnessWorkspaceRuleMarker,
		realHarnessProjectsRuleMarker,
		realHarnessCatalogRuleMarker,
		realHarnessServiceRuleMarker,
		realHarnessRootAgentMarker,
		realHarnessSkill,
		realHarnessReviewer,
		realHarnessTrustedRuntimeMarker,
		realHarnessOperatingSystemLead + runtime.GOOS,
		realHarnessArchitectureLead + runtime.GOARCH,
		realHarnessTimezoneLead + time.Now().Location().String(),
		string(workspaceMarker),
	} {
		assert.True(
			t,
			initial.markers[marker],
			"initial provider context marker %q, present in requests %v",
			marker,
			realHarnessMarkerRequestIndexes(records, marker),
		)
	}
	assert.False(t, initial.markers[realHarnessSkillBodyMarker])
	assert.False(t, initial.markers[realHarnessReviewerMarker])

	assert.True(t, realHarnessRecordContains(records[1:], realHarnessSkillBodyMarker))
	assert.True(t, realHarnessRecordContains(records[1:], realHarnessReviewerMarker))
	assert.True(t, realHarnessRecordContains(records[1:], realHarnessPatchEvent))
	assert.True(t, realHarnessRecordContains(records, realHarnessWakeHandlerMarker))
	assert.True(t, realHarnessRecordWithAll(
		records,
		[]string{realHarnessReviewerMarker, string(workspaceMarker)},
	))
}

func realHarnessRecordContains(records []realHarnessPromptRecord, marker string) bool {
	for _, record := range records {
		if record.markers[marker] {
			return true
		}
	}

	return false
}

func realHarnessMarkerRequestIndexes(
	records []realHarnessPromptRecord,
	marker string,
) []int {
	indexes := make([]int, 0)
	for index, record := range records {
		if record.markers[marker] {
			indexes = append(indexes, index)
		}
	}

	return indexes
}

func realHarnessRecordWithAll(records []realHarnessPromptRecord, markers []string) bool {
	for _, record := range records {
		all := true
		for _, marker := range markers {
			if !record.markers[marker] {
				all = false

				break
			}
		}
		if all {
			return true
		}
	}

	return false
}

func assertRealHarnessAuditLog(t *testing.T, directory string) {
	t.Helper()

	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	var content strings.Builder
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}

		fileContent, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
		require.NoError(t, readErr)
		content.WriteString(string(fileContent))
	}

	for _, message := range []string{
		"hook lifecycle event started",
		"hook group started",
		"hook action started",
		"hook command started",
		"skill activated",
		"child agent started",
		"child agent completed",
		"hook session event started",
		"event wake turn started",
		"config-remember-read",
		"record-read-state",
		"config-require-read",
		"allow-only-after-read",
		"config-allow-only-fixture-test",
		"deny-unapproved-command",
		"workspace-patch-policy",
		"workspace-patch-policy-action",
		"catalog-patch-policy",
		"catalog-patch-policy-action",
		"service-patch-event",
		"emit-patch-event",
	} {
		assert.Contains(t, content.String(), message)
	}
}
