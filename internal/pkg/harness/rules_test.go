package harness

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	rulesTestConfigAgents  = "config AGENTS"
	rulesTestRootAgents    = "root AGENTS"
	rulesTestParentAgents  = "parent AGENTS"
	rulesTestWorkspaceRule = "workspace AGENTS"
	rulesTestConfigClaude  = "config Claude rule"
	rulesTestConfigPeen    = "config Peen rule"
	rulesTestRootClaude    = "root Claude rule"
	rulesTestParentPeen    = "parent Peen rule"
	rulesTestWorkspacePeen = "workspace Peen rule"
)

func TestResolverLoadsModularRulesInLayerOrder(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	parent := filepath.Dir(fixture.workspace)
	fixture.writeAgents(t, fixture.configRoot, rulesTestConfigAgents)
	fixture.writeAgents(t, fixture.root, rulesTestRootAgents)
	fixture.writeAgents(t, parent, rulesTestParentAgents)
	fixture.writeAgents(t, fixture.workspace, rulesTestWorkspaceRule)
	fixture.writeRule(
		t,
		fixture.configRoot,
		claudeDirectoryName,
		"a.md",
		rulesTestConfigClaude,
	)
	fixture.writeRule(
		t,
		fixture.configRoot,
		agentsDirectoryName,
		"b.md",
		rulesTestConfigPeen,
	)
	fixture.writeRule(
		t,
		fixture.root,
		claudeDirectoryName,
		"a.md",
		rulesTestRootClaude,
	)
	fixture.writeRule(
		t,
		parent,
		agentsDirectoryName,
		"a.md",
		rulesTestParentPeen,
	)
	fixture.writeRule(
		t,
		fixture.workspace,
		agentsDirectoryName,
		"a.md",
		rulesTestWorkspacePeen,
	)
	writeFile(
		t,
		filepath.Join(
			fixture.workspace,
			claudeDirectoryName,
			rulesDirectoryName,
			"ignored.txt",
		),
		"not a rule",
	)

	first := resolveFixture(t, fixture)
	assert.Equal(t, []string{
		rulesTestConfigAgents,
		rulesTestConfigClaude,
		rulesTestConfigPeen,
		rulesTestRootAgents,
		rulesTestRootClaude,
		rulesTestParentAgents,
		rulesTestParentPeen,
		rulesTestWorkspaceRule,
		rulesTestWorkspacePeen,
	}, instructionContents(first.Instructions()[1:]))

	manifest := first.Manifest()
	assert.Contains(t, manifest, ManifestEntry{
		Kind: SourceKindRule,
		Source: filepath.Join(
			fixture.configRoot,
			claudeDirectoryName,
			rulesDirectoryName,
			"a.md",
		),
		Priority: 0,
		Hash:     hashString(rulesTestConfigClaude),
	})
	assert.NotContains(t, instructionContents(first.Instructions()), "not a rule")

	blocks, err := first.PromptBlocks("")
	require.NoError(t, err)
	assert.Equal(t, SourceKindRule, blocks[2].Kind)
	assert.Equal(t, rulesTestConfigClaude, blocks[2].Content)

	fixture.writeRule(
		t,
		fixture.workspace,
		agentsDirectoryName,
		"a.md",
		"changed workspace Peen rule",
	)
	second := resolveFixture(t, fixture)
	assert.NotEqual(t, first.Hash(), second.Hash())
}

func TestResolverIgnoresUnusableOptionalModularRule(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		setup func(t *testing.T, fixture resolverFixture)
	}{
		{
			name: "empty rule",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeRule(
					t,
					fixture.workspace,
					agentsDirectoryName,
					"empty.md",
					" \n\t",
				)
			},
		},
		{
			name: "directory with Markdown suffix",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				makeDirectory(t, filepath.Join(
					fixture.workspace,
					claudeDirectoryName,
					rulesDirectoryName,
					"directory.md",
				))
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := newResolverFixture(t)
			tc.setup(t, fixture)

			resolver, err := NewResolver(fixture.configRoot, Limits{})
			require.NoError(t, err)
			snapshot, resolveErr := resolver.Resolve(fixture.workspace)
			require.NoError(t, resolveErr)
			require.Len(t, snapshot.Warnings(), 1)
			assert.Equal(t, SourceKindRule, snapshot.Warnings()[0].Kind)
		})
	}
}

func (f resolverFixture) writeRule(
	t *testing.T,
	directory string,
	parentDirectory string,
	name string,
	content string,
) {
	t.Helper()

	writeFile(t, filepath.Join(
		directory,
		parentDirectory,
		rulesDirectoryName,
		name,
	), content)
}
