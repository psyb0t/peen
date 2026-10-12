package harness

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/psyb0t/peen/internal/pkg/workspacefs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveDirectoryFollowsTheCurrentScope(t *testing.T) {
	t.Parallel()
	fixture := newResolverFixture(t)
	a := filepath.Join(fixture.workspace, "a")
	b := filepath.Join(fixture.workspace, "b")
	fixture.writeAgents(t, fixture.workspace, "parent instruction")
	fixture.writeAgents(t, a, "scope-a")
	fixture.writeAgents(t, b, "scope-b")
	fixture.writeSkill(t, a, "local-skill", "a", "skill-a")
	fixture.writeSkill(t, b, "local-skill", "b", "skill-b")
	writeFile(t, filepath.Join(a, ".agents", "rules", "local.md"), "rule-a")
	writeFile(t, filepath.Join(b, ".agents", "rules", "local.md"), "rule-b")
	resolver, err := NewResolver(fixture.configRoot, Limits{})
	require.NoError(t, err)
	testCases := []struct {
		name      string
		directory string
		want      string
		notWanted string
		skill     string
	}{
		{name: "parent", directory: fixture.workspace, notWanted: "scope-a"},
		{name: "first child", directory: a, want: "scope-a", notWanted: "scope-b", skill: "skill-a"},
		{name: "sibling", directory: b, want: "scope-b", notWanted: "scope-a", skill: "skill-b"},
		{name: "return parent", directory: fixture.workspace, notWanted: "scope-b"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			snapshot, err := resolver.ResolveDirectory(tc.directory)
			require.NoError(t, err)
			blocks, err := snapshot.PromptBlocks("")
			require.NoError(t, err)
			contents := make([]string, 0, len(blocks))
			for _, block := range blocks {
				contents = append(contents, block.Content)
			}
			content := strings.Join(contents, "\n")
			assert.Contains(t, content, "parent instruction")
			if tc.want != "" {
				assert.Contains(t, content, tc.want)
			}
			assert.NotContains(t, content, tc.notWanted)
			skill, err := snapshot.ActivateSkill("local-skill")
			if tc.skill == "" {
				require.ErrorIs(t, err, ErrSkillNotFound)

				return
			}
			require.NoError(t, err)
			assert.Contains(t, skill.Content, tc.skill)
		})
	}
}

func TestResolveDirectoryWalksOnlyVirtualAncestors(t *testing.T) {
	t.Parallel()
	reader, err := workspacefs.NewMemory(map[string]string{
		"AGENTS.md":                "virtual root",
		"a/AGENTS.md":              "virtual child",
		"a/deep/AGENTS.md":         "unvisited grandchild",
		"b/AGENTS.md":              "unvisited sibling",
		"a/.agents/rules/local.md": "child rule",
	})
	require.NoError(t, err)
	resolver, err := NewResolver(t.TempDir(), Limits{}, WithWorkspaceFilesystem(workspacefs.ReadOnly{Source: reader}, true))
	require.NoError(t, err)
	snapshot, err := resolver.ResolveDirectory("a")
	require.NoError(t, err)
	contents := make([]string, 0, len(snapshot.Instructions()))
	for _, instruction := range snapshot.Instructions() {
		contents = append(contents, instruction.Content)
	}
	assert.Contains(t, contents, "virtual root")
	assert.Contains(t, contents, "virtual child")
	assert.Contains(t, contents, "child rule")
	assert.NotContains(t, contents, "unvisited grandchild")
	assert.NotContains(t, contents, "unvisited sibling")
}
