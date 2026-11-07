//go:build real

package realtest

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
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

	// workspaceRoot pins the directory a client may open. Without it the
	// controller would inherit the operator's PEEN_WORKSPACE_ROOT from the
	// deployment .env this suite reads providers from, and refuse the fixture.
	workspaceRoot string

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
// preToolUse hook. Everything outside this set is denied, which is what the
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
			"readFile,listFiles,searchText",
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
			realHarnessSkillBodyMarker+"\nUse applyPatch for mutations. Read every target before patching it. Run the fixture test after the patch.\n",
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

	return realHarnessFixture{
		configDirectory: configDirectory,
		stateDirectory:  stateDirectory,
		socketDirectory: socketDirectory,
		workspaceRoot:   workspace,
		workspace:       workspace,
		service:         service,
		implementation:  implementation,
		testFile:        testFile,
		notesFile:       notesFile,
		auditDirectory:  auditDirectory,
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
// a Docker agent space would have to solve both constraints at once.
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
postReadFile:
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
preApplyPatch:
  - name: config-require-read
    actions:
      - name: allow-only-after-read
        type: command
        command: %q
        args: ["-test.run=^TestRealHarnessHookHelper$"]
        environment:
          PEEN_REAL_HOOK_HELPER: enabled
preToolUse:
  - name: config-allow-only-fixture-test
    match:
      tool: runCommand
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
	return "version: 1\npreApplyPatch:\n  - name: " + name +
		"\n    actions:\n      - name: " + name +
		"-action\n        type: inject\n        message: " + message + "\n"
}

func realHarnessServiceHooksDocument() string {
	return fmt.Sprintf(`version: 1
preWriteFile:
  - name: service-require-patch-for-writes
    actions:
      - name: deny-direct-write
        type: deny
        reason: %q
preEditFile:
  - name: service-require-patch-for-edits
    actions:
      - name: deny-direct-edit
        type: deny
        reason: %q
postApplyPatch:
  - name: service-patch-event
    actions:
      - name: emit-patch-event
        type: emitEvent
        eventType: fixture.patch.applied
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
