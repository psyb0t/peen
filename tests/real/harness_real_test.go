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
	realHarnessTestTimeout = 20 * time.Minute

	// realHarnessIdleTimeout bounds the gap between two live events rather than
	// the whole turn. A real model doing a hundred tool rounds is slow but not
	// stuck, and an absolute deadline cannot tell those apart. Silence for this
	// long is the actual failure.
	realHarnessIdleTimeout     = 4 * time.Minute
	realHarnessStartupTimeout  = 30 * time.Second
	realHarnessShutdownTimeout = 15 * time.Second
	realHarnessPollInterval    = 50 * time.Millisecond
	realHarnessPageLimit       = 200

	realHarnessSocketDirectoryName = "pw"
	realHarnessWorkerSocketName    = "worker.sock"
	realHarnessMaxSocketPathBytes  = 107

	// realHarnessLifecycleTurns is the turn count before the wake event. One
	// user message is one turn: the child agent the task launches runs inside
	// it and must not open a turn of its own.
	realHarnessLifecycleTurns = 1

	// realHarnessMaxEvents bounds the durable event walk. It is a runaway
	// guard, not a limit on what a turn may emit.
	realHarnessMaxEvents        = 100000
	realHarnessDirectoryMode    = 0o700
	realHarnessFileMode         = 0o600
	realHarnessBinaryName       = "peen-real-harness"
	realHarnessCommandPackage   = "./cmd"
	realHarnessRunCommand       = "run"
	realHarnessNetwork          = "tcp"
	realHarnessLoopback         = "127.0.0.1:0"
	realHarnessAPIPath          = "/v1"
	realHarnessMessagesPath     = realHarnessAPIPath + "/messages"
	realHarnessWebSocketPath    = realHarnessAPIPath + "/ws"
	realHarnessSessionPath      = realHarnessAPIPath + "/session"
	realHarnessEventsPath       = realHarnessSessionPath + "/events"
	realHarnessNoticesPath      = realHarnessSessionPath + "/notices"
	realHarnessWorkersPath      = realHarnessSessionPath + "/workers"
	realHarnessMessagesListPath = realHarnessSessionPath + "/messages"
	realHarnessAgentRunsPath    = realHarnessSessionPath + "/agents"
	realHarnessJobsPath         = realHarnessSessionPath + "/jobs"
	realHarnessTurnsPath        = realHarnessSessionPath + "/turns"
	realHarnessModelRunsPath    = realHarnessSessionPath + "/model-runs"

	realHarnessContextSnapshotsPath = realHarnessSessionPath + "/context-snapshots"
	realHarnessPromptSnapshotsPath  = realHarnessSessionPath + "/prompt-snapshots"
	realHarnessReadyPath            = "/ready"
	realHarnessAuthorization        = "Authorization"
	realHarnessSessionIDHeader      = "X-Session-ID"
	realHarnessContentType          = "Content-Type"
	realHarnessBearerPrefix         = "Bearer "
	realHarnessJSONMediaType        = "application/json"
	realHarnessWebSocketSessionID   = "sessionId"
	realHarnessWebSocketProtocol    = "peen.v1"
	realHarnessWebSocketBearer      = "peen.bearer."
	realHarnessMessageSendEvent     = "message.send"
	realHarnessMessageCompleted     = "message.completed"
	realHarnessMessageFailed        = "message.failed"
	realHarnessRootAgent            = "default"
	realHarnessSkill                = "fixture-service"
	realHarnessReviewer             = "fixture-reviewer"
	realHarnessPatchEvent           = "fixture.patch.applied"
	realHarnessWakeEvent            = "fixture.wake"
	realHarnessFixtureTestCommand   = "go test ./..."

	// realHarnessLongCommand is the only other command the fixture allows. A
	// job has to still be running for the signal endpoints to have anything to
	// act on, and the fixture's test command finishes too fast for that.
	realHarnessLongCommand = "sleep 300"

	realHarnessNotesFileName = "NOTES.md"

	// realHarnessNotesLineCount makes that file big enough that one read fills
	// a tool result at the deployment's result cap.
	realHarnessNotesLineCount    = 1500
	realHarnessPatchOnlyReason   = "Fixture changes must use apply_patch."
	realHarnessCommandOnlyReason = "Only the fixture test command is allowed."
	realHarnessHookHelperEnv     = "PEEN_REAL_HOOK_HELPER"
	realHarnessHookHelperEnabled = "enabled"
	realHarnessReadMarker        = "read-observed"
	realHarnessTaskMessage       = `This is an execution task with mandatory tool-call acceptance criteria, not a request for advice. Do not return a final answer until all criteria below have completed successfully. In this exact order: (1) call use_skill with fixture-service; (2) call launch_agent with fixture-reviewer, asking it to inspect internal/status/status.go and internal/status/status_test.go without changes; (3) after the child returns, call read_file yourself for each of those two paths; (4) call apply_patch, and only apply_patch, to make Label return "ready" and make the test expect "ready"; (5) call run_command with exactly "go test ./..." from this workspace. Do not alter any other path.`

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

	// stateDirectory is the controller's own durable state. It is a sibling of
	// the configuration directory, never a child, because Peen refuses a state
	// directory that sits inside the one every worker receives read-only.
	stateDirectory string

	// socketDirectory keeps the worker socket path inside the 107-byte Unix
	// socket limit. t.TempDir() embeds the test name, which alone pushes the
	// default state/workers root past it.
	socketDirectory string

	// workspaceRootsJSON pins the directories a client may open. Without it the
	// controller would inherit the operator's PEEN_WORKSPACE_ROOTS from the
	// deployment .env this suite reads providers from, and refuse the fixture.
	workspaceRootsJSON string

	workspace      string
	service        string
	implementation string
	testFile       string

	// notesFile is large on purpose. One read of it fills a tool result, which
	// is how the compaction test crosses a context budget that a turn reading
	// the small fixture sources never would.
	notesFile string

	auditDirectory string
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
	Message string `json:"message"`
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

	// The session opens on the service directory, not the fixture root. The
	// task names paths relative to it, and the deepest AGENTS.md layer only
	// resolves for a session rooted there.
	sessionID := openRealHarnessSession(t, process, fixture.service)
	require.Equal(t, sessionID, sendRealHarnessMessage(t, process, sessionID))

	messages := listRealHarnessMessages(t, process.baseURL, process.apiToken, sessionID)
	t.Logf("real harness tool calls: %v", realHarnessToolCallNames(messages))
	assertRealHarnessTranscriptMessages(t, messages)
	assertRealHarnessTranscript(t, process.baseURL, process.apiToken, sessionID)
	assertRealHarnessAgentRun(t, process.baseURL, process.apiToken, sessionID)
	assertRealHarnessJob(t, process.baseURL, process.apiToken, sessionID, fixture.service)
	assertRealHarnessInjectedEventDelivered(t, process.baseURL, process.apiToken, sessionID)
	assertRealHarnessFiles(t, fixture)

	generationID := assertRealHarnessWorkerGeneration(
		t,
		process.baseURL,
		process.apiToken,
		sessionID,
		fixture.service,
	)
	turnIDs := assertRealHarnessDurableTurns(
		t,
		process.baseURL,
		process.apiToken,
		sessionID,
		fixture.service,
		realHarnessLifecycleTurns,
	)
	assertRealHarnessDurableEvents(
		t,
		process.baseURL,
		process.apiToken,
		sessionID,
		turnIDs,
		generationID,
	)
	assertRealHarnessModelRuns(
		t,
		process.baseURL,
		process.apiToken,
		sessionID,
		turnIDs,
		modelID,
	)
	assertRealHarnessTurnSnapshots(
		t,
		process.baseURL,
		process.apiToken,
		sessionID,
		fixture.service,
	)

	publishRealHarnessWakeEvent(t, process.baseURL, process.apiToken, sessionID)
	waitForRealHarnessWake(t, process.baseURL, process.apiToken, sessionID)

	// The wake turn runs in the same generation. A controller that restarted
	// the worker between the two turns would show a second row here.
	assert.Equal(t, generationID, assertRealHarnessWorkerGeneration(
		t,
		process.baseURL,
		process.apiToken,
		sessionID,
		fixture.service,
	))

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

		if !realHarnessCommandAllowed(input.Command) {
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

// realHarnessNotesDocument builds a file large enough that reading it fills a
// tool result, which is what pushes a session over a context budget.
func realHarnessNotesDocument() string {
	lines := make([]string, 0, realHarnessNotesLineCount)
	for index := range realHarnessNotesLineCount {
		lines = append(lines, fmt.Sprintf(
			"- note %04d: the fixture service records status label history.",
			index,
		))
	}

	return "# Status notes\n\n" + strings.Join(lines, "\n") + "\n"
}

// realHarnessCommandAllowed is the fixture's command policy, enforced by the
// pre_tool_use hook. Everything outside this set is denied, which is what the
// lifecycle test checks when it asserts a refused command.
func realHarnessCommandAllowed(command string) bool {
	return command == realHarnessFixtureTestCommand ||
		command == realHarnessLongCommand
}

func newRealHarnessFixture(t *testing.T) realHarnessFixture {
	t.Helper()

	root := newRealHarnessRoot(t)
	configDirectory := filepath.Join(root, "config")
	stateDirectory := filepath.Join(root, "state")
	workspace := filepath.Join(root, "workspace")
	service := filepath.Join(workspace, "projects", "catalog", "service")
	auditDirectory := filepath.Join(root, "audit")
	implementation := filepath.Join(service, "internal", "status", "status.go")
	testFile := filepath.Join(service, "internal", "status", "status_test.go")
	notesFile := filepath.Join(service, realHarnessNotesFileName)

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

	writeRealHarnessFile(t, notesFile, realHarnessNotesDocument())
	writeRealHarnessFile(t, filepath.Join(service, "go.mod"), "module fixture.local/service\n\ngo 1.26.6\n")
	writeRealHarnessFile(t, implementation, "package status\n\nfunc Label() string {\n\treturn \"pending\"\n}\n")
	writeRealHarnessFile(t, testFile, "package status\n\nimport \"testing\"\n\nfunc TestLabel(t *testing.T) {\n\tif got := Label(); got != \"pending\" {\n\t\tt.Fatalf(\"Label() = %q, want pending\", got)\n\t}\n}\n")

	require.NoError(t, os.MkdirAll(stateDirectory, realHarnessDirectoryMode))

	// Not under root: t.TempDir() embeds the test name, which alone pushes the
	// worker socket path past its 107 byte limit.
	socketDirectory, err := os.MkdirTemp("", realHarnessSocketDirectoryName)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(socketDirectory)) })
	assertRealHarnessSocketBudget(t, socketDirectory)

	workspaceRoots, err := json.Marshal([]string{workspace})
	require.NoError(t, err)

	return realHarnessFixture{
		configDirectory:    configDirectory,
		stateDirectory:     stateDirectory,
		socketDirectory:    socketDirectory,
		workspaceRootsJSON: string(workspaceRoots),
		workspace:          workspace,
		service:            service,
		implementation:     implementation,
		testFile:           testFile,
		notesFile:          notesFile,
		auditDirectory:     auditDirectory,
	}
}

// newRealHarnessRoot creates this test's disposable root outside the
// repository.
//
// The root must not sit under the repository. The harness resolver treats every
// ancestor of a workspace as a context layer, so a fixture inside the checkout
// inherits Peen's own .agents/ tree and fails to resolve before any turn runs.
//
// That rules out the repository-relative .testing/ root the Docker worker test
// uses to keep bind sources identical on the host. This suite mounts no Docker
// socket and launches no sibling container, so none of these directories is
// ever a bind source and the host does not need to see them. Giving this suite
// a Docker execution profile would have to solve both constraints at once.
func newRealHarnessRoot(t *testing.T) string {
	t.Helper()

	return t.TempDir()
}

// assertRealHarnessSocketBudget fails before the controller starts when the
// worker socket path would not fit.
//
// A Unix socket path is limited to 107 bytes. The controller adds one
// session-UUID directory and the socket file name under this root, and a
// deployment that overruns the limit otherwise fails deep inside worker start
// with an error that does not name the path as the cause.
func assertRealHarnessSocketBudget(t *testing.T, socketDirectory string) {
	t.Helper()

	longest := filepath.Join(
		socketDirectory,
		uuid.NewString(),
		realHarnessWorkerSocketName,
	)
	require.LessOrEqualf(
		t,
		len(longest),
		realHarnessMaxSocketPathBytes,
		"worker socket path %q needs %d bytes",
		longest,
		len(longest),
	)
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
// needs a second execution profile or a smaller context budget can ask for one
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
	process.command = exec.Command(binary, realHarnessRunCommand)
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
	// carries the operator's own roots, profiles, image, and Docker socket, and
	// any one of them reaching the spawned controller would test their
	// deployment instead of this fixture.
	for name, value := range map[string]string{
		"PEEN_CONFIG_DIR":                fixture.configDirectory,
		"PEEN_STATE_DIR":                 fixture.stateDirectory,
		"PEEN_WORKER_SOCKET_DIR":         fixture.socketDirectory,
		"PEEN_WORKSPACE_ROOTS":           fixture.workspaceRootsJSON,
		"PEEN_EXECUTION_PROFILES":        "",
		"PEEN_DEFAULT_EXECUTION_PROFILE": "",
		"PEEN_WORKER_IMAGE":              "",
		"PEEN_DOCKER_SOCKET":             "",
		"PEEN_HOST_USERNAME":             "",
		"PEEN_HOST_HOME":                 "",
		"PEEN_AGENT":                     realHarnessRootAgent,
		"PEEN_HTTP_LISTEN_ADDRESS":       apiAddress,
		"PEEN_METRICS_LISTEN_ADDRESS":    metricsAddress,
		"PEEN_UPSTREAMS":                 upstreams,
		"PEEN_DEFAULT_MODEL":             defaultModel,
		"PEEN_API_TOKEN":                 apiToken,
		"PEEN_ENABLE_WORKSPACE_HOOKS":    "true",
		"PEEN_TURN_TIMEOUT":              realHarnessTestTimeout.String(),
		"PEEN_LOG_DIRECTORY":             fixture.auditDirectory,
		"PEEN_LOG_RETENTION_DAYS":        "14",
		"LOG_LEVEL":                      "debug",
	} {
		environment[name] = name + "=" + value
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
	sessionID uuid.UUID,
) uuid.UUID {
	t.Helper()

	connection := dialRealHarnessWebSocket(t, process)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	sent := writeRealHarnessMessage(t, connection, sessionID, realHarnessTaskMessage)

	return awaitRealHarnessWebSocketCompletion(t, process, connection, sent)
}

// writeRealHarnessMessage sends one message.send frame for a named session.
//
// The control plane routes on the session in the frame metadata, never on the
// connection, so a client names the session it wants on every message.
//
// It returns the sent frame's ID. The controller echoes that ID as the
// triggeredBy of the completion it broadcasts, and because every client on a
// session receives every completion, that ID is the only way a caller can tell
// its own answer from another client's.
func writeRealHarnessMessage(
	t *testing.T,
	connection *websocket.Conn,
	sessionID uuid.UUID,
	message string,
) uuid.UUID {
	t.Helper()

	event := dabluveees.NewEvent(
		realHarnessMessageSendEvent,
		realHarnessWebSocketMessage{Message: message},
	).SetMetadata(realHarnessWebSocketSessionID, sessionID.String())
	require.NoError(t, connection.WriteJSON(event))

	return event.ID
}

// openRealHarnessSession resolves the fixture workspace to its durable session.
//
// A controller starts with no sessions, so this is where every turn in this
// suite begins. Opening the same workspace again resumes the same session.
func openRealHarnessSession(
	t *testing.T,
	process *runningRealHarnessPeen,
	workspace string,
) uuid.UUID {
	t.Helper()

	payload, err := json.Marshal(api.OpenSessionRequest{Workspace: workspace})
	require.NoError(t, err)

	request, err := http.NewRequest(
		http.MethodPost,
		process.baseURL+realHarnessAPIPath+"/sessions/open",
		bytes.NewReader(payload),
	)
	require.NoError(t, err)
	request.Header.Set(realHarnessContentType, realHarnessJSONMediaType)
	request.Header.Set(
		realHarnessAuthorization,
		realHarnessBearerPrefix+process.apiToken,
	)

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)

	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equalf(
		t,
		http.StatusOK,
		response.StatusCode,
		"Peen output:\n%s",
		process.output.String(),
	)

	opened := api.OpenedSession{}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&opened))
	require.Equal(t, workspace, opened.Session.Workspace)

	return opened.Session.Id
}

func dialRealHarnessWebSocket(
	t *testing.T,
	process *runningRealHarnessPeen,
) *websocket.Conn {
	t.Helper()

	endpoint, err := url.Parse(process.baseURL)
	require.NoError(t, err)
	endpoint.Scheme = "ws"
	endpoint.Path = realHarnessWebSocketPath

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

// awaitRealHarnessWebSocketCompletion reads until the completion that answers
// sent, and fails if that completion says the message was queued rather than
// run as its own turn.
func awaitRealHarnessWebSocketCompletion(
	t *testing.T,
	process *runningRealHarnessPeen,
	connection *websocket.Conn,
	sent uuid.UUID,
) uuid.UUID {
	t.Helper()

	sessionID, queued := awaitRealHarnessWebSocketResult(
		t,
		process,
		connection,
		sent,
	)
	require.False(t, queued)

	return sessionID
}

// awaitRealHarnessWebSocketResult reads frames until the completion triggered
// by sent arrives, and reports the session it names and whether the controller
// queued the message behind an active turn.
func awaitRealHarnessWebSocketResult(
	t *testing.T,
	process *runningRealHarnessPeen,
	connection *websocket.Conn,
	sent uuid.UUID,
) (uuid.UUID, bool) {
	t.Helper()

	for {
		event := readRealHarnessWebSocketEvent(t, process, connection)
		if event.TriggeredBy == nil || *event.TriggeredBy != sent {
			continue
		}

		switch event.Type {
		case realHarnessMessageCompleted:
			result := realHarnessWebSocketMessageResult{}
			require.NoError(t, json.Unmarshal(event.Data, &result))

			return realHarnessEventSession(t, event), result.Queued
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

// readRealHarnessWebSocketEvent reads one live frame under an idle deadline.
func readRealHarnessWebSocketEvent(
	t *testing.T,
	process *runningRealHarnessPeen,
	connection *websocket.Conn,
) dabluveees.Event {
	t.Helper()

	// Renewed per frame. A turn that keeps emitting live events is slow,
	// not stuck, and one absolute deadline cannot tell those apart.
	require.NoError(t, connection.SetReadDeadline(
		time.Now().Add(realHarnessIdleTimeout),
	))

	event := dabluveees.Event{}
	require.NoErrorf(
		t,
		connection.ReadJSON(&event),
		"Peen output:\n%s",
		process.output.String(),
	)

	return event
}

// realHarnessEventSession reads the session every live frame must name, so a
// client with many session tabs can route the frame it just received.
func realHarnessEventSession(
	t *testing.T,
	event dabluveees.Event,
) uuid.UUID {
	t.Helper()

	require.NotNil(t, event.Metadata)
	value, found := event.Metadata.Get(realHarnessWebSocketSessionID)
	require.True(t, found)
	text, isText := value.(string)
	require.True(t, isText)
	sessionID, err := uuid.Parse(text)
	require.NoError(t, err)

	return sessionID
}

func assertRealHarnessFiles(t *testing.T, fixture realHarnessFixture) {
	t.Helper()

	implementation, err := os.ReadFile(fixture.implementation)
	require.NoError(t, err)
	assert.Contains(t, string(implementation), "return \"ready\"")

	// The assertion is the comparison the test makes, not the wording of its
	// failure message. A model that updates the comparison and leaves the
	// message reading "want pending" has still made the change the task asked
	// for, and the fixture test passes either way.
	testContent, err := os.ReadFile(fixture.testFile)
	require.NoError(t, err)
	assert.Contains(t, string(testContent), "!= \"ready\"")
	assert.NotContains(t, string(testContent), "!= \"pending\"")
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
		assert.Equal(t, int64(0), job.ExitCode)
		found = true
	}

	assert.True(t, found)
}

// assertRealHarnessWorkerGeneration checks the durable record of the process
// that actually ran the turn, and returns its identifier.
//
// A native profile keeps the worker in a child process of the controller, so
// the generation must carry neither a container nor an image digest. Those
// belong to a Docker generation and their presence here would mean the
// controller launched something other than what the profile asked for.
func assertRealHarnessWorkerGeneration(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
	workspace string,
) string {
	t.Helper()

	page := getRealHarnessJSON[api.WorkerGenerationPage](
		t,
		baseURL+realHarnessWorkersPath+"?limit=50",
		apiToken,
		sessionID,
	)
	require.Len(t, page.Items, 1)

	generation := page.Items[0]
	assert.Equal(t, sessionID, generation.SessionId)
	assert.Equal(t, workspace, generation.Workspace)
	assert.Equal(t, api.WorkerGenerationKindNative, generation.Kind)
	assert.Equal(t, api.WorkerGenerationStateReady, generation.State)
	assert.Nil(t, generation.ContainerId)
	assert.Nil(t, generation.ImageDigest)
	assert.Nil(t, generation.FailureDetail)
	assert.Nil(t, generation.EndedAt)
	require.NotNil(t, generation.StartedAt)

	return generation.Id.String()
}

// assertRealHarnessDurableTurns checks the turn rows the session produced and
// returns their identifiers, so callers can check that every other durable
// record points back at a turn that really exists.
func assertRealHarnessDurableTurns(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
	workspace string,
	wantCount int,
) map[uuid.UUID]struct{} {
	t.Helper()

	page := getRealHarnessJSON[api.TurnPage](
		t,
		baseURL+realHarnessTurnsPath+"?limit=200",
		apiToken,
		sessionID,
	)
	require.Len(t, page.Turns, wantCount)

	turnIDs := make(map[uuid.UUID]struct{}, len(page.Turns))
	for _, turn := range page.Turns {
		assert.Equal(t, sessionID, turn.SessionId)
		assert.Equal(t, workspace, turn.Workspace)
		assert.Equal(t, api.TurnStateCompleted, turn.State)
		assert.False(t, turn.CancelRequested)
		assert.Empty(t, turn.FailureClassification)
		assert.NotEqual(t, uuid.Nil, turn.RequestId)
		assert.NotNil(t, turn.CompletedAt)
		turnIDs[turn.Id] = struct{}{}
	}

	return turnIDs
}

// assertRealHarnessTurnSnapshots follows a turn's context and prompt hashes to
// the snapshots they name.
//
// The hashes are how a later reader reconstructs what the model was actually
// given. A turn that stored a hash no snapshot resolves would read as complete
// and still be impossible to audit.
func assertRealHarnessTurnSnapshots(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
	service string,
) {
	t.Helper()

	page := getRealHarnessJSON[api.TurnPage](
		t,
		baseURL+realHarnessTurnsPath+"?limit=200",
		apiToken,
		sessionID,
	)
	require.NotEmpty(t, page.Turns)

	turn := page.Turns[0]
	require.NotNil(t, turn.ContextSnapshotHash)
	require.NotNil(t, turn.PromptSnapshotHash)

	context := getRealHarnessJSON[api.ContextSnapshot](
		t,
		baseURL+realHarnessContextSnapshotsPath+"/"+*turn.ContextSnapshotHash,
		apiToken,
		sessionID,
	)
	assert.Equal(t, *turn.ContextSnapshotHash, context.Hash)
	assert.NotEmpty(t, context.Manifest)
	assert.Contains(t, context.ResolvedContent, realHarnessServiceRuleMarker)

	prompt := getRealHarnessJSON[api.PromptSnapshot](
		t,
		baseURL+realHarnessPromptSnapshotsPath+"/"+*turn.PromptSnapshotHash,
		apiToken,
		sessionID,
	)
	assert.Equal(t, *turn.PromptSnapshotHash, prompt.Hash)
	assert.NotEmpty(t, prompt.EffectivePrompt)

	// The workspace the turn ran in must be the one the fixture opened, and the
	// prompt is where a wrong workspace would first show.
	assert.Contains(t, prompt.EffectivePrompt+context.ResolvedContent, service)
}

// assertRealHarnessDurableEvents walks the whole durable event stream and
// checks the identity every event must carry.
//
// The sequence check matters most: a client rebuilding a session from another
// machine replays these in order, so a gap or a repeat is a corrupt transcript
// rather than a cosmetic flaw.
func assertRealHarnessDurableEvents(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
	turnIDs map[uuid.UUID]struct{},
	generationID string,
) []api.TranscriptEvent {
	t.Helper()

	events := listRealHarnessEvents(t, baseURL, apiToken, sessionID)
	require.NotEmpty(t, events)

	previousSequence := int64(0)
	for index, event := range events {
		assert.Equalf(t, sessionID, event.SessionId, "event %d", index)
		assert.NotEqual(t, uuid.Nil, event.Id)
		assert.Greaterf(
			t,
			event.Sequence,
			previousSequence,
			"event %d type %q sequence must increase",
			index,
			event.Type,
		)
		previousSequence = event.Sequence

		if event.TurnId != uuid.Nil {
			assert.Containsf(
				t,
				turnIDs,
				event.TurnId,
				"event %d type %q names an unknown turn",
				index,
				event.Type,
			)
		}

		if event.WorkerGenerationId != nil {
			assert.Equalf(
				t,
				generationID,
				*event.WorkerGenerationId,
				"event %d type %q came from an unexpected generation",
				index,
				event.Type,
			)
		}
	}

	return events
}

// listRealHarnessEvents pages the durable event stream to its end.
//
// A real turn emits far more events than one page holds, and a check that read
// only the first page would silently stop looking exactly where a long turn
// gets interesting.
func listRealHarnessEvents(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
) []api.TranscriptEvent {
	t.Helper()

	events := make([]api.TranscriptEvent, 0, realHarnessPageLimit)
	for offset := 0; offset < realHarnessMaxEvents; offset += realHarnessPageLimit {
		page := getRealHarnessJSON[api.TranscriptEventPage](
			t,
			fmt.Sprintf(
				"%s%s?order=asc&limit=%d&offset=%d",
				baseURL,
				realHarnessEventsPath,
				realHarnessPageLimit,
				offset,
			),
			apiToken,
			sessionID,
		)

		events = append(events, page.Events...)
		if !page.HasMore {
			return events
		}
	}

	require.Failf(t, "durable event stream did not end", "read %d events", len(events))

	return nil
}

// assertRealHarnessModelRuns checks that the provider calls the turn made were
// recorded with the model, usage, and per-round accounting the session needs to
// report cost.
func assertRealHarnessModelRuns(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
	turnIDs map[uuid.UUID]struct{},
	modelID string,
) {
	t.Helper()

	page := getRealHarnessJSON[api.ModelRunPage](
		t,
		baseURL+realHarnessModelRunsPath+"?limit=200&stage=turn",
		apiToken,
		sessionID,
	)
	require.NotEmpty(t, page.ModelRuns)

	for _, run := range page.ModelRuns {
		assert.Equal(t, sessionID, run.SessionId)
		assert.Equal(t, api.ModelRunStageTurn, run.Stage)
		assert.Equal(t, api.ModelRunStateCompleted, run.State)
		assert.Contains(t, turnIDs, run.TurnId)
		assert.Equal(t, modelID, run.RequestedModelId)
		assert.NotEmpty(t, run.ConnectionName)
		assert.Empty(t, run.FailureClassification)
		assert.NotEmpty(t, run.ResponseUsage)
		assert.NotEmpty(t, run.ResponseMessages)
	}

	assertRealHarnessModelCalls(t, baseURL, apiToken, sessionID, page.ModelRuns[0])
}

// assertRealHarnessModelCalls checks the per-round provider records behind one
// model run: rounds numbered from one with no gap, and real token accounting.
func assertRealHarnessModelCalls(
	t *testing.T,
	baseURL, apiToken string,
	sessionID uuid.UUID,
	run api.ModelRun,
) {
	t.Helper()

	page := getRealHarnessJSON[api.ModelCallPage](
		t,
		fmt.Sprintf(
			"%s%s/%s/calls?limit=200",
			baseURL,
			realHarnessModelRunsPath,
			run.Id.String(),
		),
		apiToken,
		sessionID,
	)
	require.NotEmpty(t, page.Calls)

	completionTokens := int64(0)

	for index, call := range page.Calls {
		assert.Equal(t, sessionID, call.SessionId)
		assert.Equal(t, run.Id, call.ModelRunId)
		// Rounds are zero-based and the page is ordered by round, so the index
		// is the round a complete run must hold at that position.
		assert.Equalf(t, int64(index), call.Round, "round at position %d", index)
		assert.Equal(t, api.ModelCallStateCompleted, call.State)
		assert.Positivef(t, call.PromptTokens, "round %d prompt tokens", call.Round)
		assert.Positivef(t, call.TotalTokens, "round %d total tokens", call.Round)
		assert.NotEmptyf(t, call.RequestMessages, "round %d request messages", call.Round)
		assert.NotEmptyf(t, call.RequestTools, "round %d request tools", call.Round)
		assert.NotNilf(t, call.ResponseMessage, "round %d response message", call.Round)

		completionTokens += call.CompletionTokens
	}

	// Summed rather than per round, because a single round may legitimately
	// report no completion tokens while a run that produced none did not
	// record usage at all.
	assert.Positive(t, completionTokens)
}

// assertRealHarnessInjectedEventDelivered checks that a hook-emitted event
// reached the running turn's transcript.
//
// This is event injection, not user-message queueing. The two produce similar
// looking transcript entries, and the queueing contract is checked against a
// live turn in the session suite instead.
func assertRealHarnessInjectedEventDelivered(
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

	delivery := api.SessionNoticeRequestDelivery("wake")
	payload, err := json.Marshal(api.SessionNoticeRequest{
		Type:     realHarnessWakeEvent,
		Summary:  "Review the completed fixture.",
		Delivery: &delivery,
	})
	require.NoError(t, err)

	request, err := http.NewRequest(
		http.MethodPost,
		baseURL+realHarnessNoticesPath,
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
