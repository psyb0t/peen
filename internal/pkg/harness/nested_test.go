package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	nestedTestWorkspace = "workspace AGENTS"
	nestedTestAPI       = "api AGENTS"
	nestedTestAPIV2     = "api v2 AGENTS"
	nestedTestWeb       = "web AGENTS"
	nestedTestIgnored   = "must not load"
	nestedTestLimit     = 2

	nestedTestAPIDirectory     = "api"
	nestedTestVersionDirectory = "v2"
	nestedTestWebDirectory     = "web"
	nestedTestAPIV2Scope       = "api/v2"
)

// AGENTS.md files below the workspace load after every layer, parents before
// children, and each one says which part of the workspace it covers.
func TestResolverLoadsNestedInstructionsWithTheirScope(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	api := filepath.Join(fixture.workspace, nestedTestAPIDirectory)
	fixture.writeAgents(t, fixture.workspace, nestedTestWorkspace)
	fixture.writeAgents(
		t,
		filepath.Join(fixture.workspace, nestedTestWebDirectory),
		nestedTestWeb,
	)
	fixture.writeAgents(
		t,
		filepath.Join(api, nestedTestVersionDirectory),
		nestedTestAPIV2,
	)
	fixture.writeAgents(t, api, nestedTestAPI)

	snapshot := resolveFixture(t, fixture)
	instructions := filesystemInstructions(snapshot.Instructions())

	assert.Equal(t, []string{
		nestedTestWorkspace,
		nestedTestAPI,
		nestedTestAPIV2,
		nestedTestWeb,
	}, instructionContents(instructions))
	assert.Equal(t, []string{
		"",
		nestedTestAPIDirectory,
		nestedTestAPIV2Scope,
		nestedTestWebDirectory,
	}, instructionScopes(instructions))

	blocks, err := snapshot.PromptBlocks("")
	require.NoError(t, err)

	nested := blockContaining(t, blocks, nestedTestAPIV2)
	assert.Contains(t, nested.Content, nestedTestAPIV2Scope+"/"+agentsFileName)
	assert.Contains(t, nested.Content, "files under "+nestedTestAPIV2Scope+"/")
	assert.True(t, strings.HasSuffix(nested.Content, nestedTestAPIV2))

	workspaceBlock := blockContaining(t, blocks, nestedTestWorkspace)
	assert.Equal(t, nestedTestWorkspace, workspaceBlock.Content)
}

// Hidden directories, dependency trees, and symlinked directories are never
// searched for nested instructions.
func TestResolverSkipsNestedInstructionsOutsideTheProjectTree(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	outside := filepath.Join(fixture.root, "outside")
	fixture.writeAgents(t, outside, nestedTestIgnored)
	fixture.writeAgents(
		t,
		filepath.Join(fixture.workspace, ".git"),
		nestedTestIgnored,
	)
	fixture.writeAgents(
		t,
		filepath.Join(fixture.workspace, "node_modules", "lib"),
		nestedTestIgnored,
	)
	fixture.writeAgents(
		t,
		filepath.Join(fixture.workspace, "vendor"),
		nestedTestIgnored,
	)
	require.NoError(
		t,
		os.Symlink(outside, filepath.Join(fixture.workspace, "linked")),
	)
	fixture.writeAgents(
		t,
		filepath.Join(fixture.workspace, nestedTestAPIDirectory),
		nestedTestAPI,
	)

	snapshot := resolveFixture(t, fixture)

	assert.Equal(
		t,
		[]string{nestedTestAPI},
		instructionContents(filesystemInstructions(snapshot.Instructions())),
	)
	assert.Empty(t, snapshot.Warnings())
}

// A config directory inside the workspace is read once, as the config layer,
// and never again as a nested directory.
func TestResolverReadsAConfigDirectoryInsideTheWorkspaceOnce(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.configRoot = filepath.Join(fixture.workspace, "config")
	makeDirectory(t, fixture.configRoot)
	fixture.writeAgents(t, fixture.configRoot, rulesTestConfigAgents)
	fixture.writeAgents(
		t,
		filepath.Join(fixture.configRoot, nestedTestAPIDirectory),
		nestedTestIgnored,
	)
	fixture.writeAgents(
		t,
		filepath.Join(fixture.workspace, nestedTestAPIDirectory),
		nestedTestAPI,
	)

	snapshot := resolveFixture(t, fixture)

	assert.Equal(
		t,
		[]string{rulesTestConfigAgents, nestedTestAPI},
		instructionContents(filesystemInstructions(snapshot.Instructions())),
	)
}

// An empty nested file is skipped with a warning instead of failing the turn.
func TestResolverWarnsAboutAnEmptyNestedInstructionFile(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeAgents(
		t,
		filepath.Join(fixture.workspace, nestedTestAPIDirectory),
		" \n",
	)
	fixture.writeAgents(
		t,
		filepath.Join(fixture.workspace, nestedTestWebDirectory),
		nestedTestWeb,
	)

	snapshot := resolveFixture(t, fixture)

	assert.Equal(
		t,
		[]string{nestedTestWeb},
		instructionContents(filesystemInstructions(snapshot.Instructions())),
	)
	require.Len(t, snapshot.Warnings(), 1)
	assert.Equal(t, SourceKindInstruction, snapshot.Warnings()[0].Kind)
	assert.Contains(t, snapshot.Warnings()[0].Source, nestedTestAPIDirectory)
}

// Nested files past the instruction limit are left out with a warning. The
// turn still runs with the files that fit.
func TestResolverStopsLoadingNestedInstructionsAtTheLimit(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	for _, name := range []string{"a", "b", "c"} {
		fixture.writeAgents(t, filepath.Join(fixture.workspace, name), name)
	}

	resolver, err := NewResolver(
		fixture.configRoot,
		Limits{MaxInstructions: nestedTestLimit},
	)
	require.NoError(t, err)

	snapshot, err := resolver.Resolve(fixture.workspace)
	require.NoError(t, err)

	assert.Equal(
		t,
		[]string{"a", "b"},
		instructionContents(filesystemInstructions(snapshot.Instructions())),
	)
	require.Len(t, snapshot.Warnings(), 1)
	assert.Contains(
		t,
		snapshot.Warnings()[0].Source,
		filepath.Join("c", agentsFileName),
	)
}

func filesystemInstructions(instructions []Instruction) []Instruction {
	filtered := make([]Instruction, 0, len(instructions))
	for _, instruction := range instructions {
		if instruction.Source == embeddedInstructionSource {
			continue
		}

		filtered = append(filtered, instruction)
	}

	return filtered
}

func instructionScopes(instructions []Instruction) []string {
	scopes := make([]string, 0, len(instructions))
	for _, instruction := range instructions {
		scopes = append(scopes, instruction.Scope)
	}

	return scopes
}

func blockContaining(
	t *testing.T,
	blocks []PromptBlock,
	content string,
) PromptBlock {
	t.Helper()

	for _, block := range blocks {
		if strings.Contains(block.Content, content) {
			return block
		}
	}

	require.Failf(t, "no prompt block contains the content", "%q", content)

	return PromptBlock{}
}
