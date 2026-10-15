package agent

import (
	"encoding/json"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowSkillsRestoreAndForgetCompactedHistory(t *testing.T) {
	t.Parallel()
	ledger := newSkillLedgerFromHistory([]*models.Message{{
		Sequence: 1, InjectedKind: models.InjectedKindSkill,
		Content: "<skill>\n<name>old-skill</name>\n</skill>",
	}, {
		Sequence: 3, InjectedKind: models.InjectedKindSkill,
		Content: "<skill>\n<name>later-skill</name>\n</skill>",
	}})
	ledger.record("current-skill")
	encoded, err := json.Marshal(ledger.workflowState())
	require.NoError(t, err)
	var saved workflowSkillState
	require.NoError(t, json.Unmarshal(encoded, &saved))
	restored, err := restoreWorkflowSkills(&saved)
	require.NoError(t, err)
	assert.Equal(t, ledger.held(), restored.held())
	restored.forgetThrough(1)
	assert.NotContains(t, restored.held(), "old-skill")
	assert.Contains(t, restored.held(), "later-skill")
	assert.Contains(t, restored.held(), "current-skill")
	assert.Contains(t, ledger.held(), "old-skill")
	assert.EqualValues(t, 1, restored.workflowState().CompactedThrough)
}

func TestWorkflowSkillsRestoreRejectsMalformedState(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		state workflowSkillState
	}{
		{name: "negative compaction", state: workflowSkillState{
			CompactedThrough: -1,
		}},
		{name: "empty name", state: workflowSkillState{
			Delivered: []string{""},
		}},
		{name: "whitespace", state: workflowSkillState{
			Delivered: []string{" invalid "},
		}},
		{name: "zero sequence", state: workflowSkillState{
			History: []workflowSkillHistory{{Names: []string{"skill"}}},
		}},
		{name: "already compacted", state: workflowSkillState{
			CompactedThrough: 2,
			History:          []workflowSkillHistory{{Sequence: 1}},
		}},
		{name: "duplicate sequence", state: workflowSkillState{
			History: []workflowSkillHistory{{Sequence: 1}, {Sequence: 1}},
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := restoreWorkflowSkills(&tc.state)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}

func TestWorkflowSkillsSnapshotIsIndependentAndOrdered(t *testing.T) {
	t.Parallel()
	ledger := newSkillLedger("z-skill", "a-skill")
	state := ledger.workflowState()
	assert.Equal(t, []string{"a-skill", "z-skill"}, state.Delivered)
	state.Delivered[0] = "changed"
	assert.Contains(t, ledger.held(), "a-skill")
	restored, err := restoreWorkflowSkills(nil)
	require.NoError(t, err)
	assert.Empty(t, restored.held())
	var absent *skillLedger
	assert.Empty(t, absent.workflowState().Delivered)
}
