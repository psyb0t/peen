package peen

import (
	"testing"

	internalconfig "github.com/psyb0t/peen/internal/pkg/config"
	"github.com/stretchr/testify/assert"
)

// An embedder's zero takes the default target, a negative value turns the
// target off, and anything else passes through for the runtime to validate.
func TestCompactionTargetPercentMapsTheEmbedderValue(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		percent int
		want    int
	}{
		{
			name: "zero", percent: 0,
			want: internalconfig.DefaultCompactionTargetPercent,
		},
		{name: "negative", percent: -1, want: 0},
		{name: "set", percent: 70, want: 70},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, compactionTargetPercent(tc.percent))
		})
	}
}
