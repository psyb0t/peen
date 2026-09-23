package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExplicitSkillNames(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		message string
		want    []string
	}{
		{
			name:    "one reference at the start",
			message: ":planning make a plan",
			want:    []string{"planning"},
		},
		{
			name:    "punctuation and repeated references",
			message: "use :planning, then :freshness. Repeat :planning.",
			want:    []string{"planning", "freshness"},
		},
		{
			name:    "Unicode whitespace before reference",
			message: "inspect\u00a0:planning",
			want:    []string{"planning"},
		},
		{
			name:    "colon inside a token is ordinary text",
			message: "path:planning and :planning/notes",
			want:    []string{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, explicitSkillNames(tc.message))
		})
	}
}
