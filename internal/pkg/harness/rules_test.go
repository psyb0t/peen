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
		Name: "a",
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
			name: "rule directory nested too deep",
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				nested := filepath.Join(
					fixture.workspace,
					claudeDirectoryName,
					rulesDirectoryName,
				)
				for range maxRuleDirectoryDepth + 1 {
					nested = filepath.Join(nested, "deeper")
				}

				writeFile(t, filepath.Join(nested, "deep.md"), "too deep")
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

func TestParseRuleDocument(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		content   string
		wantBody  string
		wantPaths []string
		wantErr   bool
	}{
		{
			name:     "no frontmatter keeps the whole file",
			content:  "# Rule\n\nUse tabs.",
			wantBody: "# Rule\n\nUse tabs.",
		},
		{
			name:      "paths list",
			content:   "---\npaths:\n  - \"src/**/*.{ts,tsx}\"\n  - lib/**/*.ts\n---\n\nUse strict mode.",
			wantBody:  "Use strict mode.",
			wantPaths: []string{"src/**/*.{ts,tsx}", "lib/**/*.ts"},
		},
		{
			name:      "paths comma string keeps brace groups whole",
			content:   "---\npaths: \"src/*.{ts,tsx}, **/*.go\"\n---\nBody",
			wantBody:  "Body",
			wantPaths: []string{"src/*.{ts,tsx}", "**/*.go"},
		},
		{
			name:      "Cursor globs with unquoted patterns",
			content:   "---\ndescription: Go rules\nglobs: *.go,cmd/**/*.go\nalwaysApply: false\n---\nBody",
			wantBody:  "Body",
			wantPaths: []string{"*.go", "cmd/**/*.go"},
		},
		{
			name:      "Copilot applyTo",
			content:   "---\napplyTo: \"**/*.py\"\n---\nBody",
			wantBody:  "Body",
			wantPaths: []string{"**/*.py"},
		},
		{
			name:     "Cursor alwaysApply wins over globs",
			content:  "---\nglobs: \"*.go\"\nalwaysApply: true\n---\nBody",
			wantBody: "Body",
		},
		{
			name:     "other fields only",
			content:  "---\ndescription: shared\n---\nBody",
			wantBody: "Body",
		},
		{
			name:     "empty paths list",
			content:  "---\npaths: []\n---\nBody",
			wantBody: "Body",
		},
		{
			name:      "duplicate patterns collapse",
			content:   "---\npaths: \"*.go\"\nglobs: \"*.go\"\n---\nBody",
			wantBody:  "Body",
			wantPaths: []string{"*.go"},
		},
		{
			name:     "wrong type loads unscoped",
			content:  "---\npaths: 5\n---\nBody",
			wantBody: "Body",
			wantErr:  true,
		},
		{
			name:     "list of non strings loads unscoped",
			content:  "---\npaths:\n  - 5\n---\nBody",
			wantBody: "Body",
			wantErr:  true,
		},
		{
			name:     "broken YAML without scope loads unscoped",
			content:  "---\ndescription: [unclosed\n---\nBody",
			wantBody: "Body",
			wantErr:  true,
		},
		{
			name:      "invalid pattern is kept and reported",
			content:   "---\npaths:\n  - \"photos [2024/**\"\n  - \"*.jpg\"\n---\nBody",
			wantBody:  "Body",
			wantPaths: []string{"photos [2024/**", "*.jpg"},
			wantErr:   true,
		},
		{
			name:     "unterminated frontmatter is body",
			content:  "---\npaths: \"*.go\"\nBody",
			wantBody: "---\npaths: \"*.go\"\nBody",
		},
		{
			name:      "CRLF line endings",
			content:   "---\r\npaths: \"*.go\"\r\n---\r\nBody",
			wantBody:  "Body",
			wantPaths: []string{"*.go"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			document, err := parseRuleDocument(tc.content)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrInvalidInstruction)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, tc.wantBody, document.body)
			assert.Equal(t, tc.wantPaths, nilIfEmpty(document.paths))
		})
	}
}

func TestResolverScopesRulesWithPathsFrontmatter(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeRule(
		t,
		fixture.workspace,
		agentsDirectoryName,
		"go.md",
		"---\npaths:\n  - \"**/*.go\"\n---\nGo rule body",
	)
	fixture.writeRule(
		t,
		fixture.workspace,
		claudeDirectoryName,
		"always.md",
		"---\ndescription: shared\n---\nAlways rule body",
	)
	fixture.writeRule(
		t,
		fixture.configRoot,
		agentsDirectoryName,
		"docs.md",
		"---\nglobs: docs/*.md\n---\nDocs rule body",
	)
	writeFile(t, filepath.Join(
		fixture.workspace,
		claudeDirectoryName,
		rulesDirectoryName,
		"frontend",
		"react.md",
	), "---\npaths: \"web/**/*.tsx\"\n---\nReact rule body")

	snapshot := resolveFixture(t, fixture)

	blocks, err := snapshot.PromptBlocks("")
	require.NoError(t, err)

	contents := make([]string, 0, len(blocks))
	for _, block := range blocks {
		contents = append(contents, block.Content)
	}

	assert.Contains(t, contents, "Always rule body")
	assert.NotContains(t, contents, "Go rule body")
	assert.NotContains(t, contents, "Docs rule body")
	assert.NotContains(t, contents, "React rule body")
	assert.Empty(t, snapshot.Warnings())

	goFile := filepath.Join(fixture.workspace, "cmd", "app", "main.go")
	matched := snapshot.MatchingRules([]string{goFile})
	require.Len(t, matched, 1)
	assert.Equal(t, "go", matched[0].Name)
	assert.Equal(t, "Go rule body", matched[0].Content)
	assert.Equal(t, fixture.workspace, matched[0].Base)
	assert.Equal(t, []string{"**/*.go"}, matched[0].Paths)

	configMatched := snapshot.MatchingRules([]string{
		filepath.Join(fixture.workspace, "docs", "guide.md"),
	})
	require.Len(t, configMatched, 1)
	assert.Equal(t, "docs", configMatched[0].Name)
	assert.Equal(t, fixture.workspace, configMatched[0].Base)

	nested := snapshot.MatchingRules([]string{
		filepath.Join(fixture.workspace, "web", "src", "App.tsx"),
	})
	require.Len(t, nested, 1)
	assert.Equal(t, "frontend/react", nested[0].Name)

	assert.Empty(t, snapshot.MatchingRules([]string{
		filepath.Join(fixture.root, "outside.go"),
	}))
	assert.Empty(t, snapshot.MatchingRules([]string{
		filepath.Join(fixture.workspace, "README.md"),
	}))
}

func TestResolverLoadsRuleWithBrokenFrontmatterUnscoped(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeRule(
		t,
		fixture.workspace,
		agentsDirectoryName,
		"broken.md",
		"---\npaths: 5\n---\nBroken rule body",
	)

	snapshot := resolveFixture(t, fixture)

	require.Len(t, snapshot.Warnings(), 1)
	assert.Equal(t, SourceKindRule, snapshot.Warnings()[0].Kind)

	blocks, err := snapshot.PromptBlocks("")
	require.NoError(t, err)

	found := false
	for _, block := range blocks {
		if block.Content == "Broken rule body" {
			found = true
		}
	}

	assert.True(t, found, "the rule loads on every turn without its frontmatter")
}

func TestScopedRuleChangesSnapshotHashAndSurvivesCheckpoint(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeRule(
		t,
		fixture.workspace,
		agentsDirectoryName,
		"go.md",
		"---\npaths: \"**/*.go\"\n---\nGo rule body",
	)

	first := resolveFixture(t, fixture)

	encoded, err := first.Checkpoint()
	require.NoError(t, err)

	restored, err := RestoreCheckpoint(encoded)
	require.NoError(t, err)

	goFile := filepath.Join(fixture.workspace, "main.go")
	assert.Equal(t,
		first.MatchingRules([]string{goFile}),
		restored.MatchingRules([]string{goFile}),
	)

	fixture.writeRule(
		t,
		fixture.workspace,
		agentsDirectoryName,
		"go.md",
		"---\npaths: \"cmd/**/*.go\"\n---\nGo rule body",
	)

	second := resolveFixture(t, fixture)
	assert.NotEqual(t, first.Hash(), second.Hash())
	assert.Empty(t, second.MatchingRules([]string{goFile}))
}

// The rule pack in examples/rules ships for users to copy. Every file has to
// parse cleanly, stay short enough to be worth its context, and carry the
// scope its name promises.
func TestExampleRulePackParses(t *testing.T) {
	t.Parallel()

	const (
		packDirectory    = "../../../examples/rules"
		maxPackRuleLines = 40
		unscopedRule     = "security.md"
	)

	entries, err := os.ReadDir(packDirectory)
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	for _, entry := range entries {
		t.Run(entry.Name(), func(t *testing.T) {
			t.Parallel()

			content, err := os.ReadFile(filepath.Join(packDirectory, entry.Name()))
			require.NoError(t, err)

			document, err := parseRuleDocument(string(content))
			require.NoError(t, err)
			assert.NotEmpty(t, strings.TrimSpace(document.body))
			assert.LessOrEqual(t,
				strings.Count(string(content), "\n"), maxPackRuleLines)

			if entry.Name() == unscopedRule {
				assert.Empty(t, document.paths)

				return
			}

			assert.NotEmpty(t, document.paths)
		})
	}
}

func nilIfEmpty(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	return values
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
