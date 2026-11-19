package agent

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
)

// The summarizer reads an assistant message's reasoning under the same
// limit as a tool result: a negative limit means none, and a positive one
// keeps the start at a character boundary. A negative limit once sliced the
// text with it and panicked every compaction of a model that reasons.
func TestCompactionMessageCarriesReasoningWithinTheLimit(t *testing.T) {
	t.Parallel()

	const reasoning = "check the config first, then €dit"

	testCases := []struct {
		name  string
		limit int
		want  string
	}{
		{name: "no limit", limit: -1, want: reasoning},
		{
			name: "limit above the text", limit: len(reasoning) + 10,
			want: reasoning,
		},
		// The € takes bytes 29 to 31, so a limit of 30 lands inside it.
		{
			name: "limit inside a character", limit: 30,
			want: "check the config first, then ",
		},
		{name: "zero limit", limit: 0, want: ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			builder := &strings.Builder{}
			writeCompactionMessage(builder, elelem.Message{
				Role:      elelem.RoleAssistant,
				Content:   elelem.Text("done"),
				Reasoning: reasoning,
			}, tc.limit)

			written := builder.String()
			assert.Contains(t, written,
				compactionReasoningLead+tc.want+compactionLineBreak)
			assert.True(t, utf8.ValidString(written))
		})
	}
}

// A message from anyone but the assistant never carries a reasoning line.
func TestCompactionMessageReadsReasoningOnlyFromTheAssistant(t *testing.T) {
	t.Parallel()

	builder := &strings.Builder{}
	writeCompactionMessage(builder, elelem.Message{
		Role: elelem.RoleUser, Content: elelem.Text("task"),
		Reasoning: "not the model's",
	}, -1)

	assert.NotContains(t, builder.String(), compactionReasoningLead)
}
