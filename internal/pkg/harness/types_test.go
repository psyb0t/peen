package harness

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testConfigRoot   = "/config"
	testWorkspace    = "/workspace"
	testSnapshotHash = "snapshot-hash"
	testSkillName    = "sample-skill"
	testAgentName    = "sample-agent"
	testChangedValue = "changed"
)

func TestSnapshotReturnsIndependentCollections(t *testing.T) {
	t.Parallel()

	snapshot := testSnapshot()

	instructions := snapshot.Instructions()
	instructions[0].Content = testChangedValue

	skills := snapshot.Skills()
	skills[0].Metadata["source"] = testChangedValue

	agents := snapshot.Agents()
	agents[0].Description = testChangedValue

	manifest := snapshot.Manifest()
	manifest[0].Source = testChangedValue

	assert.Equal(t, "instructions", snapshot.Instructions()[0].Content)
	assert.Equal(t, "skill source", snapshot.Skills()[0].Metadata["source"])
	assert.Equal(t, "agent description", snapshot.Agents()[0].Description)
	assert.Equal(t, "/workspace/AGENTS.md", snapshot.Manifest()[0].Source)
}

func TestSnapshotContractAccessors(t *testing.T) {
	t.Parallel()

	snapshot := testSnapshot()

	assert.Equal(t, testConfigRoot, snapshot.ConfigRoot())
	assert.Equal(t, testWorkspace, snapshot.Workspace())
	assert.Equal(t, testSnapshotHash, snapshot.Hash())

	activated, err := snapshot.ActivateSkill(testSkillName)
	require.NoError(t, err)
	assert.Equal(t, "full skill content", activated.Content)
	activated.Metadata["source"] = testChangedValue
	assert.Equal(t, "skill source", snapshot.Skills()[0].Metadata["source"])

	agent, err := snapshot.Agent(testAgentName)
	require.NoError(t, err)
	assert.Equal(t, "agent instructions", agent.Instructions)

	blocks, err := snapshot.PromptBlocks(testAgentName)
	require.NoError(t, err)
	require.Len(t, blocks, 3)
	assert.Equal(t, SourceKindInstruction, blocks[0].Kind)
	assert.Equal(t, SourceKindAgent, blocks[1].Kind)
	assert.Equal(t, SourceKindSkill, blocks[2].Kind)
	assert.Contains(t, blocks[2].Content, testSkillName)

	_, err = snapshot.ActivateSkill("missing")
	require.ErrorIs(t, err, ErrSkillNotFound)

	_, err = snapshot.Agent("missing")
	require.ErrorIs(t, err, ErrAgentNotFound)

	_, err = snapshot.PromptBlocks("missing")
	require.ErrorIs(t, err, ErrAgentNotFound)
}

func TestRenderSkillCatalogueSortsWithoutMutatingInput(t *testing.T) {
	t.Parallel()

	skills := []Skill{
		{Name: "zulu", Description: "z description", Source: "/z"},
		{Name: "alpha", Description: "a description", Source: "/a"},
	}

	catalogue := renderSkillCatalogue(skills)

	assert.Equal(t, "zulu", skills[0].Name)
	assert.Equal(t, "alpha", skills[1].Name)

	alphaIndex := strings.Index(catalogue, "alpha")
	zuluIndex := strings.Index(catalogue, "zulu")
	assert.Less(t, alphaIndex, zuluIndex)
	assert.Equal(t, skillCatalogueEmpty, renderSkillCatalogue(nil))
}

func testSnapshot() Snapshot {
	return Snapshot{
		configRoot: testConfigRoot,
		workspace:  testWorkspace,
		hash:       testSnapshotHash,
		instructions: []Instruction{{
			Source:   "/workspace/AGENTS.md",
			Priority: 1,
			Content:  "instructions",
			Hash:     "instruction-hash",
		}},
		skills: []Skill{{
			Name:        testSkillName,
			Description: "skill description",
			Source:      "/workspace/.agents/skills/sample-skill/SKILL.md",
			Directory:   "/workspace/.agents/skills/sample-skill",
			Hash:        "skill-hash",
			Metadata:    map[string]string{"source": "skill source"},
		}},
		agents: []Agent{{
			Name:         testAgentName,
			Description:  "agent description",
			Source:       "/workspace/.agents/agents/sample-agent.md",
			Instructions: "agent instructions",
			Hash:         "agent-hash",
		}},
		manifest: []ManifestEntry{{
			Kind:     SourceKindInstruction,
			Source:   "/workspace/AGENTS.md",
			Priority: 1,
			Hash:     "instruction-hash",
		}},
		skillContents: map[string]string{testSkillName: "full skill content"},
	}
}
