package agent

import (
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
)

func cacheHints(history []elelem.Message) []elelem.CacheHint {
	hints := make([]elelem.CacheHint, len(history))
	for index, message := range history {
		hints[index] = message.CacheHint
	}

	return hints
}

func TestMarkCacheBreakpointsMarksAnchorRoundAndEnd(t *testing.T) {
	t.Parallel()

	short, none := elelem.CacheHintShort, elelem.CacheHintNone

	testCases := []struct {
		name  string
		roles []elelem.Role
		want  []elelem.CacheHint
	}{
		{name: "empty"},
		{
			name: "one message", roles: []elelem.Role{elelem.RoleUser},
			want: []elelem.CacheHint{short},
		},
		{
			name: "tool round",
			roles: []elelem.Role{
				elelem.RoleUser, elelem.RoleAssistant, elelem.RoleTool,
				elelem.RoleAssistant, elelem.RoleTool, elelem.RoleTool,
			},
			want: []elelem.CacheHint{short, none, none, short, none, short},
		},
		{
			name: "no assistant yet",
			roles: []elelem.Role{
				elelem.RoleUser, elelem.RoleUser, elelem.RoleUser,
			},
			want: []elelem.CacheHint{short, none, short},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			history := make([]elelem.Message, len(tc.roles))
			for index, role := range tc.roles {
				history[index] = elelem.Message{
					Role: role, Content: elelem.Text("x"),
				}
			}

			markCacheBreakpoints(history)

			if len(tc.want) == 0 {
				assert.Empty(t, history)

				return
			}

			assert.Equal(t, tc.want, cacheHints(history))
		})
	}
}

func TestSavedHistoryDropsCacheBreakpoints(t *testing.T) {
	t.Parallel()

	history := []elelem.Message{
		{Role: elelem.RoleUser, Content: elelem.Text("a")},
		{Role: elelem.RoleAssistant, Content: elelem.Text("b")},
		{Role: elelem.RoleUser, Content: elelem.Text("c")},
	}
	markCacheBreakpoints(history)

	for _, message := range savedHistory(history) {
		assert.Equal(t, elelem.CacheHintNone, message.CacheHint)
	}
}
