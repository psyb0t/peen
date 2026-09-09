package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testDirectoryMode   = 0o700
	testFileMode        = 0o600
	testInstructionBody = "follow the local rules"
	testSkillBody       = "Use this skill when requested."
	testAgentBody       = "Work on the assigned task."
	testDocumentFormat  = `---
name: %s
description: %s
---
%s`
	testUnknownSkillFrontMatter = `---
name: valid-skill
description: valid
unknown: value
---
`
	testMetadataScalarFrontMatter = `---
name: metadata-skill
description: valid
metadata: value
---
`
	testUserInvocableStringFrontMatter = `---
name: invocable-skill
description: valid
user-invocable: "true"
---
`
	testCodexSkillFrontMatter = `---
name: codex-skill
description: valid
homepage: https://example.invalid
user-invocable: true
permissions:
  filesystem:
    read:
      - "**/*.go"
metadata:
  openclaw:
    requires:
      bins:
        - docker
---
`
	testNamedAgentLicenseFrontMatter = `---
name: valid-agent
description: valid
license: MIT
---
`
)

type resolverFixture struct {
	root       string
	configRoot string
	workspace  string
}

func TestResolverOrdersLayersAndReplacesEffectiveDefinitions(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeAgents(t, fixture.configRoot, "config rules")
	fixture.writeAgents(t, fixture.root, "root rules")
	fixture.writeAgents(t, filepath.Dir(fixture.workspace), "parent rules")
	fixture.writeAgents(t, fixture.workspace, "workspace rules")
	fixture.writeSkill(
		t,
		fixture.configRoot,
		"shared-skill",
		"config",
		testSkillBody,
	)
	fixture.writeSkill(
		t,
		fixture.workspace,
		"shared-skill",
		"workspace",
		"workspace skill",
	)
	fixture.writeAgent(
		t,
		fixture.configRoot,
		"shared-agent",
		"config",
		testAgentBody,
	)
	fixture.writeAgent(
		t,
		fixture.workspace,
		"shared-agent",
		"workspace",
		"workspace agent",
	)

	snapshot := resolveFixture(t, fixture)

	instructions := snapshot.Instructions()
	assert.Equal(t, []string{
		"config rules",
		"root rules",
		"parent rules",
		"workspace rules",
	}, instructionContents(instructions[1:]))
	assert.Equal(t, embeddedInstructionSource, instructions[0].Source)
	assert.Equal(t, embeddedInstructionPriority, instructions[0].Priority)
	assert.True(
		t,
		strictlyIncreasing(instructionPriorities(instructions)),
	)
	activatedSkill, err := snapshot.ActivateSkill("shared-skill")
	require.NoError(t, err)
	assert.Equal(t, "workspace", activatedSkill.Description)
	assert.Equal(
		t,
		skillDocument("shared-skill", "workspace", "workspace skill"),
		activatedSkill.Content,
	)
	sharedAgent, err := snapshot.Agent("shared-agent")
	require.NoError(t, err)
	assert.Equal(t, "workspace", sharedAgent.Description)
	assert.Equal(t, "workspace agent", sharedAgent.Instructions)
}

func TestResolverIncludesEmbeddedBaseHarness(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	snapshot := resolveFixture(t, fixture)

	instructions := snapshot.Instructions()
	require.Len(t, instructions, 1)
	assert.Equal(t, embeddedInstructionSource, instructions[0].Source)
	assert.Equal(t, embeddedInstructionPriority, instructions[0].Priority)
	assert.Contains(t, instructions[0].Content, "trusted runtime context")
	assert.Equal(t, []string{"freshness", "planning"}, skillNames(snapshot.Skills()))
	assert.Equal(t, []string{embeddedDefaultAgentName}, agentNames(snapshot.Agents()))

	manifest := snapshot.Manifest()
	require.Len(t, manifest, 4)
	assert.Equal(t, embeddedInstructionSource, manifest[0].Source)
	assert.Equal(t, embeddedSkillsSourcePrefix+"freshness/SKILL.md", manifest[1].Source)
	assert.Equal(t, embeddedSkillsSourcePrefix+"planning/SKILL.md", manifest[2].Source)
	assert.Equal(
		t,
		embeddedAgentsSourcePrefix+embeddedDefaultAgentName+agentsFileExtension,
		manifest[3].Source,
	)
}

func TestResolverFilesystemDefaultAgentReplacesEmbeddedDefault(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeAgent(
		t,
		fixture.configRoot,
		embeddedDefaultAgentName,
		"configured default",
		testAgentBody,
	)

	limits := defaultLimits()
	limits.MaxAgents = 1
	resolver, err := NewResolver(fixture.configRoot, limits)
	require.NoError(t, err)
	snapshot, err := resolver.Resolve(fixture.workspace)
	require.NoError(t, err)

	defaultAgent, err := snapshot.Agent(embeddedDefaultAgentName)
	require.NoError(t, err)
	assert.Equal(t, "configured default", defaultAgent.Description)
	assert.Equal(t, testAgentBody, defaultAgent.Instructions)
	assert.Equal(
		t,
		filepath.Join(
			fixture.configRoot,
			agentsDirectoryName,
			agentsSubdirectory,
			embeddedDefaultAgentName+agentsFileExtension,
		),
		defaultAgent.Source,
	)
}

func TestResolverNamedAgentParsesAllowedTools(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeAgentDocument(
		t,
		fixture.configRoot,
		"restricted-agent",
		`---
name: restricted-agent
description: restricted
allowed-tools: list_files, read_file, use_skill
---
Inspect only.`,
	)

	snapshot := resolveFixture(t, fixture)
	agent, err := snapshot.Agent("restricted-agent")
	require.NoError(t, err)
	assert.Equal(
		t,
		[]string{"list_files", "read_file", "use_skill"},
		agent.AllowedTools,
	)
}

func TestResolverFilesystemSkillsReplaceEmbeddedDefinitions(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeSkill(
		t,
		fixture.configRoot,
		"planning",
		"config planning",
		"config content",
	)
	fixture.writeSkill(
		t,
		fixture.workspace,
		"planning",
		"workspace planning",
		"workspace content",
	)

	limits := defaultLimits()
	limits.MaxSkills = 1
	resolver, err := NewResolver(fixture.configRoot, limits)
	require.NoError(t, err)
	snapshot, err := resolver.Resolve(fixture.workspace)
	require.NoError(t, err)

	planning, err := snapshot.ActivateSkill("planning")
	require.NoError(t, err)
	assert.Equal(t, "workspace planning", planning.Description)
	assert.Contains(t, planning.Content, "workspace content")
	assert.NotContains(t, planning.Content, "config content")
	assert.Equal(
		t,
		filepath.Join(
			fixture.workspace,
			agentsDirectoryName,
			skillsDirectoryName,
			"planning",
			skillFileName,
		),
		planning.Source,
	)
	assert.Contains(t, snapshot.Manifest(), ManifestEntry{
		Kind:   SourceKindSkill,
		Name:   "planning",
		Source: planning.Source,
		Hash:   planning.Hash,
	})
}

func TestResolverProducesStableAndChangingHashes(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	skillsDirectory := filepath.Join(
		fixture.configRoot,
		agentsDirectoryName,
		skillsDirectoryName,
	)
	fixture.writeSkill(t, fixture.configRoot, "zulu", "z", testSkillBody)
	fixture.writeSkill(t, fixture.configRoot, "alpha", "a", testSkillBody)

	first := resolveFixture(t, fixture)
	require.NoError(t, os.RemoveAll(skillsDirectory))
	fixture.writeSkill(t, fixture.configRoot, "alpha", "a", testSkillBody)
	fixture.writeSkill(t, fixture.configRoot, "zulu", "z", testSkillBody)
	second := resolveFixture(t, fixture)

	assert.Equal(t, first.Hash(), second.Hash())
	assert.Equal(
		t,
		[]string{"alpha", "freshness", "planning", "zulu"},
		skillNames(second.Skills()),
	)

	fixture.writeAgents(t, fixture.workspace, "changed rules")
	third := resolveFixture(t, fixture)
	assert.NotEqual(t, second.Hash(), third.Hash())
}

func TestResolverHandlesMissingInputsAndCanonicalPaths(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	empty := resolveFixture(t, fixture)
	require.Len(t, empty.Instructions(), 1)
	assert.Equal(t, embeddedInstructionSource, empty.Instructions()[0].Source)
	assert.Equal(t, []string{"freshness", "planning"}, skillNames(empty.Skills()))
	assert.Equal(t, []string{embeddedDefaultAgentName}, agentNames(empty.Agents()))

	actualConfigRoot := filepath.Join(fixture.root, "actual-config")
	actualWorkspace := filepath.Join(fixture.root, "actual-workspace")

	makeDirectory(t, actualConfigRoot)
	makeDirectory(t, actualWorkspace)

	configLink := filepath.Join(fixture.root, "config-link")
	workspaceLink := filepath.Join(fixture.root, "workspace-link")

	require.NoError(t, os.Symlink(actualConfigRoot, configLink))
	require.NoError(t, os.Symlink(actualWorkspace, workspaceLink))
	fixture.writeAgents(t, actualConfigRoot, "config rules")
	fixture.writeAgents(t, actualWorkspace, "workspace rules")

	resolver, err := NewResolver(configLink, Limits{})
	require.NoError(t, err)
	snapshot, err := resolver.Resolve(workspaceLink)
	require.NoError(t, err)
	assert.Equal(t, actualConfigRoot, snapshot.ConfigRoot())
	assert.Equal(t, actualWorkspace, snapshot.Workspace())
	assert.Equal(t, []string{"config rules", "workspace rules"},
		instructionContents(snapshot.Instructions()[1:]))
}

func TestExpandHome(t *testing.T) {
	// t.Setenv mutates process-global state (HOME), so this test cannot run
	// in parallel with anything else that reads or sets it.
	home := t.TempDir()
	t.Setenv("HOME", home)

	testCases := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{
			name:  "no tilde is unchanged",
			input: filepath.Join("relative", "project"),
			want:  filepath.Join("relative", "project"),
		},
		{
			name:  "bare tilde expands to home",
			input: "~",
			want:  home,
		},
		{
			name:  "tilde slash expands to home relative path",
			input: "~/project",
			want:  filepath.Join(home, "project"),
		},
		{
			name:  "tilde slash with nested path expands",
			input: "~/a/b/c",
			want:  filepath.Join(home, "a", "b", "c"),
		},
		{
			name:  "literal tilde not at the start is unchanged",
			input: filepath.Join("a", "~b", "c"),
			want:  filepath.Join("a", "~b", "c"),
		},
		{
			name:  "trailing literal tilde is unchanged",
			input: "project~",
			want:  "project~",
		},
		{
			name:    "another user's home is rejected",
			input:   "~user",
			wantErr: ErrUnsupportedHomeReference,
		},
		{
			name:    "another user's home with a subpath is rejected",
			input:   "~user/project",
			wantErr: ErrUnsupportedHomeReference,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := expandHome(tc.input)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestResolverExpandsTildeConfigRootAndWorkspace(t *testing.T) {
	// t.Setenv mutates process-global state (HOME), so this test cannot run
	// in parallel with anything else that reads or sets it.
	home := t.TempDir()
	t.Setenv("HOME", home)

	configRoot := filepath.Join(home, "config")
	workspace := filepath.Join(home, "work", "workspace")
	makeDirectory(t, configRoot)
	makeDirectory(t, workspace)
	writeFile(
		t,
		filepath.Join(configRoot, agentsFileName),
		"config rules",
	)

	resolver, err := NewResolver("~/config", Limits{})
	require.NoError(t, err)

	snapshot, err := resolver.Resolve("~/work/workspace")
	require.NoError(t, err)
	assert.Equal(t, configRoot, snapshot.ConfigRoot())
	assert.Equal(t, workspace, snapshot.Workspace())
}

func TestResolverFollowsSkillSymlinks(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	actualSkillDirectory := filepath.Join(fixture.root, "source-skill")
	fixture.writeSkillAt(
		t,
		actualSkillDirectory,
		"linked-skill",
		"source",
		testSkillBody,
	)
	linkedSkillDirectory := filepath.Join(
		fixture.configRoot,
		agentsDirectoryName,
		skillsDirectoryName,
		"linked-skill",
	)
	makeDirectory(t, filepath.Dir(linkedSkillDirectory))
	require.NoError(t, os.Symlink(actualSkillDirectory, linkedSkillDirectory))

	snapshot := resolveFixture(t, fixture)
	linkedSkill, err := snapshot.ActivateSkill("linked-skill")
	require.NoError(t, err)
	assert.Equal(t, actualSkillDirectory, linkedSkill.Directory)
	assert.Equal(
		t,
		filepath.Join(actualSkillDirectory, skillFileName),
		linkedSkill.Source,
	)
}

func TestResolverFollowsNamedAgentSymlinks(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	actualAgentPath := filepath.Join(fixture.root, "source-agent.md")
	writeFile(
		t,
		actualAgentPath,
		agentDocument("linked-agent", "source", testAgentBody),
	)

	linkedAgentPath := filepath.Join(
		fixture.configRoot,
		agentsDirectoryName,
		agentsSubdirectory,
		"linked-agent.md",
	)
	makeDirectory(t, filepath.Dir(linkedAgentPath))
	require.NoError(t, os.Symlink(actualAgentPath, linkedAgentPath))

	snapshot := resolveFixture(t, fixture)
	linkedAgent, err := snapshot.Agent("linked-agent")
	require.NoError(t, err)
	assert.Equal(t, actualAgentPath, linkedAgent.Source)
}

func TestResolverAcceptsCodexSkillFrontMatter(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeSkillDocument(
		t,
		fixture.configRoot,
		"codex-skill",
		testCodexSkillFrontMatter+testSkillBody,
	)

	snapshot := resolveFixture(t, fixture)
	skill, err := snapshot.ActivateSkill("codex-skill")
	require.NoError(t, err)
	assert.Equal(t, "https://example.invalid", skill.Homepage)
	assert.True(t, skill.UserInvocable)

	filesystem, ok := skill.Permissions["filesystem"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{"**/*.go"}, filesystem["read"])

	openclaw, ok := skill.Metadata["openclaw"].(map[string]any)
	require.True(t, ok)
	requires, ok := openclaw["requires"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{"docker"}, requires["bins"])
}

func TestResolverRejectsMalformedDiscoveredInputs(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		setup   func(t *testing.T, fixture resolverFixture)
		wantErr error
	}{
		{
			name: "empty instruction file",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()
				fixture.writeAgents(t, fixture.configRoot, "   ")
			},
			wantErr: ErrInvalidInstruction,
		},
		{
			name: "skill missing frontmatter",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()
				fixture.writeSkillDocument(
					t,
					fixture.configRoot,
					"valid-skill",
					testSkillBody,
				)
			},
			wantErr: ErrInvalidSkill,
		},
		{
			name: "skill unknown frontmatter field",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()
				fixture.writeSkillDocument(
					t,
					fixture.configRoot,
					"valid-skill",
					testUnknownSkillFrontMatter+testSkillBody,
				)
			},
			wantErr: ErrInvalidSkill,
		},
		{
			name: "skill directory name mismatch",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()
				fixture.writeSkill(
					t,
					fixture.configRoot,
					"directory-name",
					"valid",
					testSkillBody,
				)
				fixture.writeSkillDocument(
					t,
					fixture.configRoot,
					"directory-name",
					skillDocument("other-name", "valid", testSkillBody),
				)
			},
			wantErr: ErrInvalidSkill,
		},
		{
			name: "skill specification name rejects consecutive hyphens",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeSkillDocument(
					t,
					fixture.configRoot,
					"bad--skill",
					skillDocument("bad--skill", "valid", testSkillBody),
				)
			},
			wantErr: ErrInvalidSkill,
		},
		{
			name: "skill description exceeds Agent Skills limit",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				description := strings.Repeat("x", maxSkillDescriptionLength+1)
				fixture.writeSkillDocument(
					t,
					fixture.configRoot,
					"long-description",
					skillDocument(
						"long-description",
						description,
						testSkillBody,
					),
				)
			},
			wantErr: ErrInvalidSkill,
		},
		{
			name: "skill metadata must be a mapping",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeSkillDocument(
					t,
					fixture.configRoot,
					"metadata-skill",
					testMetadataScalarFrontMatter+testSkillBody,
				)
			},
			wantErr: ErrInvalidSkill,
		},
		{
			name: "skill user invocable must be a boolean",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeSkillDocument(
					t,
					fixture.configRoot,
					"invocable-skill",
					testUserInvocableStringFrontMatter+testSkillBody,
				)
			},
			wantErr: ErrInvalidSkill,
		},
		{
			name: "skill compatibility is empty when present",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeSkillDocument(
					t,
					fixture.configRoot,
					"empty-compatibility",
					`---
name: empty-compatibility
description: valid
compatibility: ""
---
Use this skill when requested.`,
				)
			},
			wantErr: ErrInvalidSkill,
		},
		{
			name: "named agent has unsupported frontmatter",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeAgentDocument(
					t,
					fixture.configRoot,
					"valid-agent",
					testNamedAgentLicenseFrontMatter+testAgentBody,
				)
			},
			wantErr: ErrInvalidAgent,
		},
		{
			name: "named agent file name mismatch",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeAgentDocument(
					t,
					fixture.configRoot,
					"file-name",
					agentDocument("other-agent", "valid", testAgentBody),
				)
			},
			wantErr: ErrInvalidAgent,
		},
		{
			name: "named agent name must be lowercase kebab case",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeAgentDocument(
					t,
					fixture.configRoot,
					"Upper-Agent",
					agentDocument("Upper-Agent", "valid", testAgentBody),
				)
			},
			wantErr: ErrInvalidAgent,
		},
		{
			name: "skill file is missing",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				makeDirectory(
					t,
					filepath.Join(
						fixture.configRoot,
						agentsDirectoryName,
						skillsDirectoryName,
						"missing-skill-file",
					),
				)
			},
			wantErr: ErrInvalidSkill,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := newResolverFixture(t)
			tc.setup(t, fixture)
			resolver, err := NewResolver(fixture.configRoot, Limits{})
			require.NoError(t, err)
			_, err = resolver.Resolve(fixture.workspace)
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestResolverEnforcesFileAndEffectiveDefinitionBounds(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		limits  func(Limits) Limits
		setup   func(t *testing.T, fixture resolverFixture)
		wantErr error
	}{
		{
			name: "file count accepts equality",
			limits: func(limits Limits) Limits {
				limits.MaxFiles = 2

				return limits
			},
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeAgents(t, fixture.configRoot, "config")
				fixture.writeAgents(t, fixture.workspace, "workspace")
			},
		},
		{
			name: "file count rejects one more",
			limits: func(limits Limits) Limits {
				limits.MaxFiles = 1

				return limits
			},
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeAgents(t, fixture.configRoot, "config")
				fixture.writeAgents(t, fixture.workspace, "workspace")
			},
			wantErr: ErrResourceLimit,
		},
		{
			name: "instruction count rejects one more",
			limits: func(limits Limits) Limits {
				limits.MaxInstructions = 1

				return limits
			},
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeAgents(t, fixture.configRoot, "config")
				fixture.writeAgents(t, fixture.workspace, "workspace")
			},
			wantErr: ErrResourceLimit,
		},
		{
			name: "skill count accepts replacement",
			limits: func(limits Limits) Limits {
				limits.MaxSkills = 1

				return limits
			},
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeSkill(
					t,
					fixture.configRoot,
					"same-skill",
					"config",
					testSkillBody,
				)
				fixture.writeSkill(
					t,
					fixture.workspace,
					"same-skill",
					"workspace",
					testSkillBody,
				)
			},
		},
		{
			name: "skill count rejects a second effective name",
			limits: func(limits Limits) Limits {
				limits.MaxSkills = 1

				return limits
			},
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeSkill(
					t,
					fixture.configRoot,
					"first-skill",
					"first",
					testSkillBody,
				)
				fixture.writeSkill(
					t,
					fixture.workspace,
					"second-skill",
					"second",
					testSkillBody,
				)
			},
			wantErr: ErrResourceLimit,
		},
		{
			name: "agent count accepts replacement",
			limits: func(limits Limits) Limits {
				limits.MaxAgents = 1

				return limits
			},
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeAgent(
					t,
					fixture.configRoot,
					"same-agent",
					"config",
					testAgentBody,
				)
				fixture.writeAgent(
					t,
					fixture.workspace,
					"same-agent",
					"workspace",
					testAgentBody,
				)
			},
		},
		{
			name: "agent count rejects a second effective name",
			limits: func(limits Limits) Limits {
				limits.MaxAgents = 1

				return limits
			},
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeAgent(
					t,
					fixture.configRoot,
					"first-agent",
					"first",
					testAgentBody,
				)
				fixture.writeAgent(
					t,
					fixture.workspace,
					"second-agent",
					"second",
					testAgentBody,
				)
			},
			wantErr: ErrResourceLimit,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := newResolverFixture(t)
			tc.setup(t, fixture)
			resolver, err := NewResolver(
				fixture.configRoot,
				tc.limits(defaultLimits()),
			)
			require.NoError(t, err)

			_, err = resolver.Resolve(fixture.workspace)
			if tc.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestResolverEnforcesByteBoundsAtEquality(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		limits  func(Limits, resolverFixture) Limits
		setup   func(t *testing.T, fixture resolverFixture)
		wantErr error
	}{
		{
			name: "individual file accepts equality",
			limits: func(limits Limits, _ resolverFixture) Limits {
				content := agentDocument(
					"single-agent",
					"single",
					testAgentBody,
				)
				limits.MaxFileBytes = int64(len(content))

				return limits
			},
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeAgent(
					t,
					fixture.configRoot,
					"single-agent",
					"single",
					testAgentBody,
				)
			},
		},
		{
			name: "individual file rejects one byte more",
			limits: func(limits Limits, _ resolverFixture) Limits {
				content := agentDocument(
					"single-agent",
					"single",
					testAgentBody,
				)
				limits.MaxFileBytes = int64(len(content) - 1)

				return limits
			},
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeAgent(
					t,
					fixture.configRoot,
					"single-agent",
					"single",
					testAgentBody,
				)
			},
			wantErr: ErrResourceLimit,
		},
		{
			name: "total context accepts equality",
			limits: func(limits Limits, _ resolverFixture) Limits {
				configContent := agentDocument(
					"config-agent",
					"config",
					testAgentBody,
				)
				workspaceContent := agentDocument(
					"workspace-agent",
					"workspace",
					testAgentBody,
				)
				limits.MaxFileBytes = int64(
					max(len(configContent), len(workspaceContent)),
				)
				limits.MaxTotalContextBytes = int64(
					len(configContent) + len(workspaceContent),
				)

				return limits
			},
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeAgent(
					t,
					fixture.configRoot,
					"config-agent",
					"config",
					testAgentBody,
				)
				fixture.writeAgent(
					t,
					fixture.workspace,
					"workspace-agent",
					"workspace",
					testAgentBody,
				)
			},
		},
		{
			name: "total context rejects one byte more",
			limits: func(limits Limits, _ resolverFixture) Limits {
				configContent := agentDocument(
					"config-agent",
					"config",
					testAgentBody,
				)
				workspaceContent := agentDocument(
					"workspace-agent",
					"workspace",
					testAgentBody,
				)
				limits.MaxFileBytes = int64(
					max(len(configContent), len(workspaceContent)),
				)
				limits.MaxTotalContextBytes = int64(
					len(configContent) + len(workspaceContent) - 1,
				)

				return limits
			},
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeAgent(
					t,
					fixture.configRoot,
					"config-agent",
					"config",
					testAgentBody,
				)
				fixture.writeAgent(
					t,
					fixture.workspace,
					"workspace-agent",
					"workspace",
					testAgentBody,
				)
			},
			wantErr: ErrResourceLimit,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := newResolverFixture(t)
			limits := tc.limits(defaultLimits(), fixture)
			tc.setup(t, fixture)

			resolver, err := NewResolver(fixture.configRoot, limits)
			require.NoError(t, err)

			_, err = resolver.Resolve(fixture.workspace)
			if tc.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestResolveNonexistentWorkspaceReturnsInvalidPath(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	resolver, err := NewResolver(fixture.configRoot, Limits{})
	require.NoError(t, err)

	missing := filepath.Join(fixture.root, "does-not-exist")

	_, err = resolver.Resolve(missing)
	require.ErrorIs(t, err, ErrInvalidPath)
}

func TestResolveUnreadableWorkspaceReturnsUnreadableLayer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not enforced the same way on windows")
	}

	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory mode bits")
	}

	t.Parallel()

	fixture := newResolverFixture(t)
	locked := filepath.Join(fixture.root, "locked")
	makeDirectory(t, locked)
	require.NoError(t, os.Chmod(locked, 0o000))

	t.Cleanup(func() {
		_ = os.Chmod(locked, 0o700) //nolint:gosec // needs exec bit restored so t.TempDir() cleanup can traverse and remove the locked directory
	})

	resolver, err := NewResolver(fixture.configRoot, Limits{})
	require.NoError(t, err)

	_, err = resolver.Resolve(locked)
	require.ErrorIs(t, err, ErrUnreadableLayer)
}

func newResolverFixture(t *testing.T) resolverFixture {
	t.Helper()

	root := t.TempDir()
	fixture := resolverFixture{
		root:       root,
		configRoot: filepath.Join(root, "config"),
		workspace:  filepath.Join(root, "work", "workspace"),
	}
	makeDirectory(t, fixture.configRoot)
	makeDirectory(t, fixture.workspace)

	return fixture
}

func resolveFixture(t *testing.T, fixture resolverFixture) Snapshot {
	t.Helper()

	resolver, err := NewResolver(fixture.configRoot, Limits{})
	require.NoError(t, err)
	snapshot, err := resolver.Resolve(fixture.workspace)
	require.NoError(t, err)

	return snapshot
}

func (f resolverFixture) writeAgents(
	t *testing.T,
	directory string,
	content string,
) {
	t.Helper()

	writeFile(t, filepath.Join(directory, agentsFileName), content)
}

func (f resolverFixture) writeSkill(
	t *testing.T,
	directory string,
	name string,
	description string,
	body string,
) {
	t.Helper()

	f.writeSkillDocument(
		t,
		directory,
		name,
		skillDocument(name, description, body),
	)
}

func (f resolverFixture) writeSkillAt(
	t *testing.T,
	directory string,
	name string,
	description string,
	body string,
) {
	t.Helper()

	writeFile(
		t,
		filepath.Join(directory, skillFileName),
		skillDocument(name, description, body),
	)
}

func (f resolverFixture) writeSkillDocument(
	t *testing.T,
	directory string,
	name string,
	content string,
) {
	t.Helper()

	writeFile(
		t,
		filepath.Join(
			directory,
			agentsDirectoryName,
			skillsDirectoryName,
			name,
			skillFileName,
		),
		content,
	)
}

func (f resolverFixture) writeAgent(
	t *testing.T,
	directory string,
	name string,
	description string,
	body string,
) {
	t.Helper()

	f.writeAgentDocument(
		t,
		directory,
		name,
		agentDocument(name, description, body),
	)
}

func (f resolverFixture) writeAgentDocument(
	t *testing.T,
	directory string,
	name string,
	content string,
) {
	t.Helper()

	writeFile(
		t,
		filepath.Join(
			directory,
			agentsDirectoryName,
			agentsSubdirectory,
			name+agentsFileExtension,
		),
		content,
	)
}

func skillDocument(name string, description string, body string) string {
	return fmt.Sprintf(testDocumentFormat, name, description, body)
}

func agentDocument(name string, description string, body string) string {
	return fmt.Sprintf(testDocumentFormat, name, description, body)
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()

	makeDirectory(t, filepath.Dir(path))
	require.NoError(t, os.WriteFile(path, []byte(content), testFileMode))
}

func makeDirectory(t *testing.T, path string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(path, testDirectoryMode))
}

func instructionContents(instructions []Instruction) []string {
	contents := make([]string, 0, len(instructions))
	for _, instruction := range instructions {
		contents = append(contents, instruction.Content)
	}

	return contents
}

func instructionPriorities(instructions []Instruction) []int {
	priorities := make([]int, 0, len(instructions))
	for _, instruction := range instructions {
		priorities = append(priorities, instruction.Priority)
	}

	return priorities
}

func strictlyIncreasing(values []int) bool {
	for index := 1; index < len(values); index++ {
		if values[index] <= values[index-1] {
			return false
		}
	}

	return true
}

func skillNames(skills []Skill) []string {
	names := make([]string, 0, len(skills))
	for _, skill := range skills {
		names = append(names, skill.Name)
	}

	return names
}

func agentNames(agents []Agent) []string {
	names := make([]string, 0, len(agents))
	for _, agent := range agents {
		names = append(names, agent.Name)
	}

	return names
}
