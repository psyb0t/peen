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
	skillTestCoreName    = "planning"

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

	output, err := useSkillHandler(snapshot, nil)(
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

	output, err := useSkillHandler(snapshot, nil)(
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

	output, err := useSkillHandler(fixture.resolve(t), nil)(
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

	output, err := useSkillHandler(fixture.resolve(t), nil)(
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
			wantSubstring: harness.CorePrefix + "freshness",
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

			output, err := useSkillHandler(snapshot, nil)(
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

func TestUseSkillHandlerResolvesCoreAndCustomSkillsOfOneName(t *testing.T) {
	t.Parallel()

	const (
		coreName   = harness.CorePrefix + skillTestCoreName
		customBody = "Custom planning body."
	)

	fixture := newSkillTestFixture(t)
	fixture.writeSkill(
		t,
		fixture.workspace,
		skillTestCoreName,
		skillTestWorkspaceDesc,
		customBody,
	)
	handler := useSkillHandler(fixture.resolve(t), nil)

	plain, err := handler(
		context.Background(),
		useSkillInput{Name: skillTestCoreName},
	)
	require.NoError(t, err)
	assert.Equal(t, skillTestCoreName, plain.Name)
	assert.Contains(t, plain.Content, customBody)

	core, err := handler(
		context.Background(),
		useSkillInput{Name: coreName},
	)
	require.NoError(t, err)
	assert.Equal(t, coreName, core.Name)
	assert.NotContains(t, core.Content, customBody)
}

func TestUseSkillHandlerPlainNameFallsBackToCoreSkill(t *testing.T) {
	t.Parallel()

	output, err := useSkillHandler(newSkillTestFixture(t).resolve(t), nil)(
		context.Background(),
		useSkillInput{Name: skillTestCoreName},
	)

	require.NoError(t, err)
	assert.Equal(t, harness.CorePrefix+skillTestCoreName, output.Name)
}

func TestUseSkillHandlerRefusesADisabledCoreSkill(t *testing.T) {
	t.Parallel()

	fixture := newSkillTestFixture(t)
	resolver, err := harness.NewResolver(
		fixture.configRoot,
		harness.Limits{},
		harness.WithDisabledCoreSkills([]string{skillTestCoreName}),
	)
	require.NoError(t, err)

	snapshot, err := resolver.Resolve(fixture.workspace)
	require.NoError(t, err)

	handler := useSkillHandler(snapshot, nil)

	for _, name := range []string{
		skillTestCoreName,
		harness.CorePrefix + skillTestCoreName,
	} {
		_, err := handler(context.Background(), useSkillInput{Name: name})
		require.ErrorIs(t, err, harness.ErrSkillNotFound, name)
	}

	_, err = handler(
		context.Background(),
		useSkillInput{Name: harness.CorePrefix + "freshness"},
	)
	require.NoError(t, err)
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
	handler := useSkillHandler(snapshot, nil)

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

	set := hostToolSet(newToolTestExecutor(t), nil, snapshot, nil, nil)

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
			EventTypeUserMessageCreated,
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

// referencedSkillFixture writes four config skills: alpha names bravo and
// delta, and bravo names charlie.
func referencedSkillFixture(t *testing.T) harness.Snapshot {
	t.Helper()

	fixture := newSkillTestFixture(t)
	bodies := map[string]string{
		"ref-alpha":   "Alpha, with :ref-bravo and :ref-delta.",
		"ref-bravo":   "Bravo, with :ref-charlie.",
		"ref-charlie": "Charlie.",
		"ref-delta":   "Delta.",
	}

	for name, body := range bodies {
		fixture.writeSkill(t, fixture.configRoot, name, name+" skill", body)
	}

	return fixture.resolve(t)
}

func TestUseSkillDescriptionDocumentsReferencedSkills(t *testing.T) {
	t.Parallel()

	assert.Contains(t, useSkillDescription, "referencedSkills")
	assert.Contains(t, useSkillDescription, ":name")
	assert.Contains(t, useSkillDescription, "already")
}

func referencedNames(output useSkillOutput) []string {
	names := make([]string, 0, len(output.ReferencedSkills))
	for _, skill := range output.ReferencedSkills {
		names = append(names, skill.Name)
	}

	return names
}

func TestUseSkillReturnsReferencedSkillsDepthFirst(t *testing.T) {
	t.Parallel()

	snapshot := referencedSkillFixture(t)

	output, err := useSkillHandler(snapshot, newSkillLedger())(
		context.Background(),
		useSkillInput{Name: "ref-alpha"},
	)
	require.NoError(t, err)

	assert.Equal(t, "ref-alpha", output.Name)
	assert.Equal(
		t,
		[]string{"ref-bravo", "ref-charlie", "ref-delta"},
		referencedNames(output),
	)

	for _, skill := range output.ReferencedSkills {
		activated, err := snapshot.ActivateSkill(skill.Name)
		require.NoError(t, err)
		assert.Equal(t, activated.Content, skill.Content)
		assert.Equal(t, activated.Directory, skill.Directory)
	}
}

func TestUseSkillLeavesOutReferencedSkillsTheConversationHolds(t *testing.T) {
	t.Parallel()

	snapshot := referencedSkillFixture(t)

	output, err := useSkillHandler(snapshot, newSkillLedger("ref-bravo"))(
		context.Background(),
		useSkillInput{Name: "ref-alpha"},
	)
	require.NoError(t, err)

	assert.Equal(t, []string{"ref-delta"}, referencedNames(output))
}

func TestUseSkillRecordsWhatItReturned(t *testing.T) {
	t.Parallel()

	ledger := newSkillLedger()
	handler := useSkillHandler(referencedSkillFixture(t), ledger)

	_, err := handler(context.Background(), useSkillInput{Name: "ref-alpha"})
	require.NoError(t, err)

	assert.Equal(
		t,
		map[string]struct{}{
			"ref-alpha":   {},
			"ref-bravo":   {},
			"ref-charlie": {},
			"ref-delta":   {},
		},
		ledger.held(),
	)

	again, err := handler(context.Background(), useSkillInput{Name: "ref-alpha"})
	require.NoError(t, err)
	assert.Contains(t, again.Content, "Alpha")
	assert.Empty(t, again.ReferencedSkills)
}

func TestUseSkillWithoutReferencesOmitsTheReferencedSkillsField(t *testing.T) {
	t.Parallel()

	output, err := useSkillHandler(referencedSkillFixture(t), nil)(
		context.Background(),
		useSkillInput{Name: "ref-delta"},
	)
	require.NoError(t, err)

	encoded, err := json.Marshal(output)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "referencedSkills")
}
