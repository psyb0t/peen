package harness

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	learnedTestUserRule      = "user rule body"
	learnedTestLearnedRule   = "learned rule body"
	learnedTestScopedRule    = "---\npaths:\n  - \"**/*.go\"\n---\nlearned Go rule"
	learnedTestGlobalRule    = "global learned rule body"
	learnedTestSkillName     = "deploy-app"
	learnedTestUserSkill     = "user deploy procedure"
	learnedTestLearnedSkill  = "learned deploy procedure"
	learnedTestSkillSummary  = "Deploy the app."
	learnedTestLearnedPrefix = "learned/"
)

func learnedDirectory(directory string) string {
	return filepath.Join(directory, agentsDirectoryName, learnedDirectoryName)
}

func writeLearnedRule(
	t *testing.T,
	learned string,
	name string,
	content string,
) {
	t.Helper()

	writeFile(t, filepath.Join(learned, rulesDirectoryName, name), content)
}

func writeLearnedSkill(t *testing.T, learned string, body string) {
	t.Helper()

	writeFile(
		t,
		filepath.Join(
			learned,
			skillsDirectoryName,
			learnedTestSkillName,
			skillFileName,
		),
		skillDocument(learnedTestSkillName, learnedTestSkillSummary, body),
	)
}

func findInstruction(
	t *testing.T,
	snapshot Snapshot,
	name string,
) Instruction {
	t.Helper()

	for _, instruction := range snapshot.Instructions() {
		if instruction.Name == name {
			return instruction
		}
	}

	require.Failf(t, "instruction not found", "name %s", name)

	return Instruction{}
}

func findSkill(t *testing.T, snapshot Snapshot, name string) Skill {
	t.Helper()

	for _, skill := range snapshot.Skills() {
		if skill.Name == name {
			return skill
		}
	}

	require.Failf(t, "skill not found", "name %s", name)

	return Skill{}
}

func TestResolverLoadsWorkspaceLearnedRulesAfterUserRules(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	learned := learnedDirectory(fixture.workspace)
	fixture.writeRule(
		t,
		fixture.workspace,
		agentsDirectoryName,
		"style.md",
		learnedTestUserRule,
	)
	writeLearnedRule(t, learned, "style.md", learnedTestLearnedRule)
	writeLearnedRule(t, learned, "go.md", learnedTestScopedRule)
	writeLearnedSkill(t, learned, learnedTestLearnedSkill)

	snapshot := resolveFixture(t, fixture)
	contents := instructionContents(snapshot.Instructions())

	userIndex := slices.Index(contents, learnedTestUserRule)
	learnedIndex := slices.Index(contents, learnedTestLearnedRule)
	require.GreaterOrEqual(t, userIndex, 0)
	require.GreaterOrEqual(t, learnedIndex, 0)
	assert.Less(t, userIndex, learnedIndex)

	style := findInstruction(t, snapshot, learnedTestLearnedPrefix+"style")
	assert.Equal(
		t,
		filepath.Join(learned, rulesDirectoryName, "style.md"),
		style.Source,
	)

	matched := snapshot.MatchingRules([]string{
		filepath.Join(fixture.workspace, "cmd", "main.go"),
	})
	require.Len(t, matched, 1)
	assert.Equal(t, learnedTestLearnedPrefix+"go", matched[0].Name)
	assert.Equal(t, fixture.workspace, matched[0].Base)

	skill := findSkill(t, snapshot, learnedTestSkillName)
	assert.Equal(
		t,
		filepath.Join(
			learned,
			skillsDirectoryName,
			learnedTestSkillName,
			skillFileName,
		),
		skill.Source,
	)
	assert.Empty(t, snapshot.Warnings())
}

func TestLearnedSkillNeverReplacesAUserSkill(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeSkill(
		t,
		fixture.configRoot,
		learnedTestSkillName,
		learnedTestSkillSummary,
		learnedTestUserSkill,
	)
	writeLearnedSkill(
		t,
		learnedDirectory(fixture.workspace),
		learnedTestLearnedSkill,
	)

	snapshot := resolveFixture(t, fixture)

	skill := findSkill(t, snapshot, learnedTestSkillName)
	assert.Equal(
		t,
		filepath.Join(
			fixture.configRoot,
			agentsDirectoryName,
			skillsDirectoryName,
			learnedTestSkillName,
			skillFileName,
		),
		skill.Source,
	)

	warnings := snapshot.Warnings()
	require.Len(t, warnings, 1)
	assert.Equal(t, SourceKindSkill, warnings[0].Kind)
	assert.Contains(t, warnings[0].Reason, "does not replace")
}

func TestUserSkillReplacesALearnedSkill(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	writeLearnedSkill(
		t,
		learnedDirectory(fixture.configRoot),
		learnedTestLearnedSkill,
	)
	fixture.writeSkill(
		t,
		fixture.workspace,
		learnedTestSkillName,
		learnedTestSkillSummary,
		learnedTestUserSkill,
	)

	snapshot := resolveFixture(t, fixture)

	skill := findSkill(t, snapshot, learnedTestSkillName)
	assert.Equal(
		t,
		filepath.Join(
			fixture.workspace,
			agentsDirectoryName,
			skillsDirectoryName,
			learnedTestSkillName,
			skillFileName,
		),
		skill.Source,
	)
	assert.Empty(t, snapshot.Warnings())
}

func TestResolverReadsTheGlobalLearnedDirectory(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		learned       func(fixture resolverFixture) string
		wantInstances int
	}{
		{
			name: "separate directory",
			learned: func(fixture resolverFixture) string {
				return filepath.Join(fixture.root, "learned")
			},
			wantInstances: 1,
		},
		{
			name: "config layer learned directory",
			learned: func(fixture resolverFixture) string {
				return learnedDirectory(fixture.configRoot)
			},
			wantInstances: 1,
		},
		{
			name: "missing directory",
			learned: func(fixture resolverFixture) string {
				return filepath.Join(fixture.root, "missing")
			},
			wantInstances: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := newResolverFixture(t)
			learned := tc.learned(fixture)

			if tc.wantInstances > 0 {
				writeLearnedRule(t, learned, "global.md", learnedTestGlobalRule)
			}

			resolver, err := NewResolver(
				fixture.configRoot,
				Limits{},
				WithLearnedDirectory(learned),
			)
			require.NoError(t, err)

			snapshot, err := resolver.Resolve(fixture.workspace)
			require.NoError(t, err)

			contents := instructionContents(snapshot.Instructions())
			assert.Equal(
				t,
				tc.wantInstances,
				countOf(contents, learnedTestGlobalRule),
			)
			assert.Empty(t, snapshot.Warnings())

			if tc.wantInstances == 0 {
				return
			}

			global := findInstruction(
				t,
				snapshot,
				learnedTestLearnedPrefix+"global",
			)
			assert.Equal(t, 0, global.Priority)
		})
	}
}

func countOf(values []string, want string) int {
	count := 0

	for _, value := range values {
		if value == want {
			count++
		}
	}

	return count
}
