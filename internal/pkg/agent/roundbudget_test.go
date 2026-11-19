package agent

import (
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const roundBudgetTestMaxRounds = 32

// The round budget note reaches the newest tool result or user message on
// exactly the request that has three rounds left, and never changes the
// message the saved history holds.
func TestRoundBudgetNoteFiresOnceWhenThreeRoundsRemain(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		round     int
		maxRounds int
		role      elelem.Role
		wantNote  bool
	}{
		{
			name: "three rounds left after a tool result", round: 29,
			maxRounds: roundBudgetTestMaxRounds, role: elelem.RoleTool,
			wantNote: true,
		},
		{
			name: "three rounds left after a user message", round: 29,
			maxRounds: roundBudgetTestMaxRounds, role: elelem.RoleUser,
			wantNote: true,
		},
		{
			name: "four rounds left", round: 28,
			maxRounds: roundBudgetTestMaxRounds, role: elelem.RoleTool,
		},
		{
			name: "two rounds left", round: 30,
			maxRounds: roundBudgetTestMaxRounds, role: elelem.RoleTool,
		},
		{
			name: "budget no larger than the warning", round: 0,
			maxRounds: roundBudgetWarningRounds, role: elelem.RoleTool,
		},
		{
			name: "newest message from the model", round: 29,
			maxRounds: roundBudgetTestMaxRounds, role: elelem.RoleAssistant,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// The saved content has spare capacity, so an append that did
			// not clone would write the note into backing[1].
			backing := make(elelem.Content, 2)
			backing[0] = elelem.TextOf("result")
			saved := backing[:1]
			history := []elelem.Message{{Role: tc.role, Content: saved}}

			addRoundBudgetNote(history, tc.round, tc.maxRounds)

			require.Len(t, saved, 1)
			assert.Equal(t, "result", backing[0].Text)
			assert.Empty(t, backing[1].Text,
				"the saved content must not grow in place")

			if !tc.wantNote {
				assert.Equal(t, "result", history[0].Text())

				return
			}

			assert.Equal(t, "result\n"+roundBudgetNote, history[0].Text())
		})
	}
}

// The saved conversation keeps what the tool returned: the note travels on
// the request alone.
func TestSavedHistoryDropsTheRoundBudgetNote(t *testing.T) {
	t.Parallel()

	history := []elelem.Message{
		{Role: elelem.RoleUser, Content: elelem.Text("task")},
		{Role: elelem.RoleTool, ToolCallID: "call", Content: elelem.Text("ok")},
	}

	addRoundBudgetNote(history, 29, roundBudgetTestMaxRounds)
	require.Contains(t, history[1].Text(), roundBudgetNote)

	saved := savedHistory(history)

	require.Len(t, saved, 2)
	assert.Equal(t, "task", saved[0].Text())
	assert.Equal(t, "ok", saved[1].Text())
	assert.Len(t, saved[1].Content, 1)
}

func TestRoundBudgetNoteIgnoresAnEmptyHistory(t *testing.T) {
	t.Parallel()

	assert.NotPanics(t, func() {
		addRoundBudgetNote(nil, 29, roundBudgetTestMaxRounds)
	})
}
