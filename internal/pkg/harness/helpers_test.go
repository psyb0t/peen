package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type resolverFixture struct {
	root       string
	configRoot string
	workspace  string
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

func warningKinds(warnings []Warning) []SourceKind {
	values := make([]SourceKind, 0, len(warnings))
	for _, warning := range warnings {
		values = append(values, warning.Kind)
	}

	return values
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

func resolveWithOptions(
	t *testing.T,
	fixture resolverFixture,
	options ...ResolverOption,
) Snapshot {
	t.Helper()

	resolver, err := NewResolver(fixture.configRoot, Limits{}, options...)
	require.NoError(t, err)
	snapshot, err := resolver.Resolve(fixture.workspace)
	require.NoError(t, err)

	return snapshot
}

// allCoreSkillNames is every embedded core skill, sorted.
func allCoreSkillNames() []string {
	return []string{
		embeddedDelegationSkillName,
		embeddedFreshnessSkillName,
		embeddedPlanAndDelegateSkillName,
		embeddedPlanningSkillName,
		embeddedWritingRulesSkillName,
		embeddedWritingSkillsSkillName,
	}
}

// allCoreSkillRefs is every embedded core skill under its core: name, sorted.
func allCoreSkillRefs() []string {
	return []string{
		testCoreDelegationName,
		testCoreFreshnessName,
		testCorePlanDelegateName,
		testCorePlanningName,
		testCoreWritingRules,
		testCoreWritingSkills,
	}
}
