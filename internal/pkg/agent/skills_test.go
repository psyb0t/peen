package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	skillTestSharedName = "shared-skill"
	skillTestConfigDesc = "config version"
	skillTestConfigBody = "Config skill body."

	skillTestWorkspaceDesc = "workspace version"
	skillTestWorkspaceBody = "Workspace skill body."

	skillTestOtherName = "config-only-skill"
	skillTestOtherDesc = "config only"
	skillTestOtherBody = "Config only skill body."

	skillTestUnknownName = "does-not-exist"

	skillTestDocumentFormat = "---\nname: %s\ndescription: %s\n---\n%s"

	skillTestCallID    = "call_use_skill"
	skillTestFinalText = "used it"
)

type skillTestFixture struct {
	configRoot string
	workspace  string
}

func newSkillTestFixture(t *testing.T) skillTestFixture {
	t.Helper()

	root := t.TempDir()
	fixture := skillTestFixture{
		configRoot: filepath.Join(root, "config"),
		workspace:  filepath.Join(root, "work", "workspace"),
	}

	require.NoError(
		t,
		os.MkdirAll(fixture.configRoot, runtimeTestDirectoryMode),
	)
	require.NoError(
		t,
		os.MkdirAll(fixture.workspace, runtimeTestDirectoryMode),
	)

	return fixture
}

func (f skillTestFixture) writeSkill(
	t *testing.T,
	directory string,
	name string,
	description string,
	body string,
) {
	t.Helper()

	content := fmt.Sprintf(skillTestDocumentFormat, name, description, body)
	writeRuntimeFile(
		t,
		filepath.Join(directory, ".agents", "skills", name, "SKILL.md"),
		content,
	)
}

func (f skillTestFixture) resolve(t *testing.T) harness.Snapshot {
	t.Helper()

	resolver, err := harness.NewResolver(f.configRoot, harness.Limits{})
	require.NoError(t, err)

	snapshot, err := resolver.Resolve(f.workspace)
	require.NoError(t, err)

	return snapshot
}

func TestUseSkillHandlerResolvesConfigRootSkill(t *testing.T) {
	t.Parallel()

	fixture := newSkillTestFixture(t)
	fixture.writeSkill(
		t,
		fixture.configRoot,
		skillTestOtherName,
		skillTestOtherDesc,
		skillTestOtherBody,
	)
	snapshot := fixture.resolve(t)

	output, err := useSkillHandler(snapshot)(
		context.Background(),
		useSkillInput{Name: skillTestOtherName},
	)
	require.NoError(t, err)
	assert.Equal(t, skillTestOtherName, output.Name)
	assert.Equal(
		t,
		fmt.Sprintf(
			skillTestDocumentFormat,
			skillTestOtherName,
			skillTestOtherDesc,
			skillTestOtherBody,
		),
		output.Content,
	)
	// The exact directory, not just its suffix: a suffix check passes no
	// matter which layer the skill actually came from, and the directory is
	// what bundled references and scripts resolve against.
	assert.Equal(
		t,
		resolvedSkillDirectory(t, fixture.configRoot, skillTestOtherName),
		output.Directory,
	)
	assert.NotEmpty(t, output.Hash)
}

func TestUseSkillHandlerWorkspaceLayerReplacesConfigRootSkill(t *testing.T) {
	t.Parallel()

	fixture := newSkillTestFixture(t)
	fixture.writeSkill(
		t,
		fixture.configRoot,
		skillTestSharedName,
		skillTestConfigDesc,
		skillTestConfigBody,
	)
	fixture.writeSkill(
		t,
		fixture.workspace,
		skillTestSharedName,
		skillTestWorkspaceDesc,
		skillTestWorkspaceBody,
	)
	snapshot := fixture.resolve(t)

	output, err := useSkillHandler(snapshot)(
		context.Background(),
		useSkillInput{Name: skillTestSharedName},
	)
	require.NoError(t, err)
	assert.Equal(
		t,
		fmt.Sprintf(
			skillTestDocumentFormat,
			skillTestSharedName,
			skillTestWorkspaceDesc,
			skillTestWorkspaceBody,
		),
		output.Content,
		"the later, more specific layer must replace the config-root copy",
	)
	assert.NotContains(t, output.Content, skillTestConfigBody)

	// The directory has to follow the winning layer too, not just the
	// content. A skill's bundled references and scripts are resolved relative
	// to this path, so content from one layer paired with a directory from
	// another would run the wrong copy's files while looking correct.
	assert.Equal(
		t,
		resolvedSkillDirectory(t, fixture.workspace, skillTestSharedName),
		output.Directory,
		"the winning layer's directory must be the one reported",
	)
	assert.NotEqual(
		t,
		resolvedSkillDirectory(t, fixture.configRoot, skillTestSharedName),
		output.Directory,
	)
}

// resolvedSkillDirectory builds the on-disk skill directory for one layer,
// symlink-resolved the way the harness canonicalizes it.
func resolvedSkillDirectory(t *testing.T, layer, name string) string {
	t.Helper()

	directory := filepath.Join(layer, ".agents", "skills", name)

	resolved, err := filepath.EvalSymlinks(directory)
	require.NoError(t, err)

	return resolved
}

// The reported directory has to be absolute. It is what a caller passes as
// run_command's directory to launch a bundled script, and a relative path
// would resolve against the message workspace instead of the skill.
func TestUseSkillHandlerReportsAnAbsoluteDirectory(t *testing.T) {
	t.Parallel()

	fixture := newSkillTestFixture(t)
	fixture.writeSkill(
		t,
		fixture.workspace,
		skillTestOtherName,
		skillTestOtherDesc,
		skillTestOtherBody,
	)

	output, err := useSkillHandler(fixture.resolve(t))(
		context.Background(),
		useSkillInput{Name: skillTestOtherName},
	)
	require.NoError(t, err)
	assert.True(
		t,
		filepath.IsAbs(output.Directory),
		"skill directory %q must be absolute",
		output.Directory,
	)
}

// A bundled script has to be reachable from the reported directory, because
// that is the whole point of returning it.
func TestUseSkillHandlerDirectoryLocatesBundledFiles(t *testing.T) {
	t.Parallel()

	const scriptBody = "#!/bin/sh\necho bundled\n"

	fixture := newSkillTestFixture(t)
	fixture.writeSkill(
		t,
		fixture.workspace,
		skillTestOtherName,
		skillTestOtherDesc,
		skillTestOtherBody,
	)
	writeRuntimeFile(
		t,
		filepath.Join(
			fixture.workspace,
			".agents", "skills", skillTestOtherName,
			"scripts", "run.sh",
		),
		scriptBody,
	)

	output, err := useSkillHandler(fixture.resolve(t))(
		context.Background(),
		useSkillInput{Name: skillTestOtherName},
	)
	require.NoError(t, err)

	bundled, err := os.ReadFile(
		filepath.Join(output.Directory, "scripts", "run.sh"),
	)
	require.NoError(t, err, "the reported directory must locate bundled files")
	assert.Equal(t, scriptBody, string(bundled))
}

func TestUseSkillHandlerUnknownNameListsEffectiveSkills(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		withSkill     bool
		wantSubstring string
	}{
		{
			name:          "unknown name among known skills",
			withSkill:     true,
			wantSubstring: skillTestOtherName,
		},
		{
			name:          "only embedded skills are available",
			withSkill:     false,
			wantSubstring: "freshness",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := newSkillTestFixture(t)
			if tc.withSkill {
				fixture.writeSkill(
					t,
					fixture.configRoot,
					skillTestOtherName,
					skillTestOtherDesc,
					skillTestOtherBody,
				)
			}

			snapshot := fixture.resolve(t)

			output, err := useSkillHandler(snapshot)(
				context.Background(),
				useSkillInput{Name: skillTestUnknownName},
			)
			require.ErrorIs(t, err, harness.ErrSkillNotFound)
			assert.Contains(t, err.Error(), skillTestUnknownName)
			assert.Contains(t, err.Error(), tc.wantSubstring)
			assert.Equal(t, useSkillOutput{}, output)
		})
	}
}

func TestUseSkillHandlerRepeatedCallReturnsIdenticalContent(t *testing.T) {
	t.Parallel()

	fixture := newSkillTestFixture(t)
	fixture.writeSkill(
		t,
		fixture.configRoot,
		skillTestOtherName,
		skillTestOtherDesc,
		skillTestOtherBody,
	)
	snapshot := fixture.resolve(t)
	handler := useSkillHandler(snapshot)

	first, err := handler(
		context.Background(),
		useSkillInput{Name: skillTestOtherName},
	)
	require.NoError(t, err)

	second, err := handler(
		context.Background(),
		useSkillInput{Name: skillTestOtherName},
	)
	require.NoError(t, err)

	assert.Equal(t, first, second)
}

// The tool must round-trip through the same JSON decode/encode path every
// other host tool uses, not just the bare handler function.
func TestHostToolSetUseSkillEndToEnd(t *testing.T) {
	t.Parallel()

	fixture := newSkillTestFixture(t)
	fixture.writeSkill(
		t,
		fixture.configRoot,
		skillTestOtherName,
		skillTestOtherDesc,
		skillTestOtherBody,
	)
	snapshot := fixture.resolve(t)

	set := hostToolSet(newToolTestExecutor(t), nil, snapshot, nil)

	tool, ok := set.Get(toolNameUseSkill)
	require.True(t, ok)

	schema := map[string]any{}
	require.NoError(t, json.Unmarshal(tool.ArgumentsSchema, &schema))
	assert.Equal(t, false, schema["additionalProperties"])
	assert.Equal(t, []any{"name"}, schema["required"])

	result, err := tool.Handler(context.Background(), elelem.ToolInput{
		Name:      toolNameUseSkill,
		CallID:    skillTestCallID,
		Arguments: json.RawMessage(`{"name":"` + skillTestOtherName + `"}`),
	})
	require.NoError(t, err)
	require.False(t, result.IsError)

	output := useSkillOutput{}
	require.NoError(t, json.Unmarshal([]byte(result.Content), &output))
	assert.Equal(t, skillTestOtherName, output.Name)
	assert.Contains(t, output.Content, skillTestOtherBody)
}

// One scripted use_skill call must produce a paired tool_use and tool_result
// on the event stream and a durable tool-role message, all sharing Elelem's
// call ID, exactly like the host file tools in runtime_tools_test.go.
func TestRuntimeRunExecutesUseSkillAndPairsCallID(t *testing.T) {
	arguments, err := json.Marshal(useSkillInput{Name: skillTestOtherName})
	require.NoError(t, err)

	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(skillTestCallID, toolNameUseSkill, string(arguments)),
		elelemtest.Text(skillTestFinalText),
	))
	writeRuntimeFile(
		t,
		filepath.Join(
			fixture.workspace,
			".agents",
			"skills",
			skillTestOtherName,
			"SKILL.md",
		),
		fmt.Sprintf(
			skillTestDocumentFormat,
			skillTestOtherName,
			skillTestOtherDesc,
			skillTestOtherBody,
		),
	)

	events := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "use the skill",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err)
	assert.Equal(t, skillTestFinalText, result.Text)

	assert.Equal(
		t,
		[]string{
			EventTypeTurnStarted,
			EventTypeToolUse,
			EventTypeToolResult,
			EventTypeTurnCompleted,
		},
		harnessEventTypes(events),
	)

	use := toolUsePayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolUse),
		&use,
	))
	assert.Equal(t, skillTestCallID, use.CallID)
	assert.Equal(t, toolNameUseSkill, use.Name)

	toolResult := toolResultPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolResult),
		&toolResult,
	))
	assert.Equal(t, skillTestCallID, toolResult.CallID)
	assert.False(t, toolResult.IsError)

	output := useSkillOutput{}
	require.NoError(t, json.Unmarshal([]byte(toolResult.Content), &output))
	assert.Contains(t, output.Content, skillTestOtherBody)

	messages, err := fixture.store.ListMessages(
		context.Background(),
		result.SessionID,
		session.ListMessagesOptions{Order: session.PageOrderAscending},
	)
	require.NoError(t, err)

	toolMessage := messages.Items[2]
	assert.Equal(t, skillTestCallID, toolMessage.ToolCallID)
	assert.False(t, toolMessage.IsError)
}
