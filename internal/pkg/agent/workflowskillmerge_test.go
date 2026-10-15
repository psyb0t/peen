package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowSkillMergeRetainsParallelDeliveries(t *testing.T) {
	t.Parallel()
	first := &workflowSkillState{Delivered: []string{"first"}}
	second := &workflowSkillState{Delivered: []string{"second"}}
	merged, err := mergeWorkflowSkills(first, second)
	require.NoError(t, err)
	assert.Equal(t, []string{"first", "second"}, merged.Delivered)
	merged.Delivered[0] = "changed"
	assert.Equal(t, "first", first.Delivered[0])
}

func TestWorkflowSkillMergeCannotReviveCompactedSkills(t *testing.T) {
	t.Parallel()
	old := &workflowSkillState{History: []workflowSkillHistory{{
		Sequence: 1, Names: []string{"compacted-skill"},
	}}}
	compacted := &workflowSkillState{CompactedThrough: 1}
	for _, pair := range [][2]*workflowSkillState{
		{old, compacted}, {compacted, old},
	} {
		merged, err := mergeWorkflowSkills(pair[0], pair[1])
		require.NoError(t, err)
		assert.Empty(t, merged.History)
		assert.EqualValues(t, 1, merged.CompactedThrough)
	}
}
