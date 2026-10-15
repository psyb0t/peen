package agent

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowToolResultBounds(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		content string
		options workflowToolOptions
	}{
		{
			name: "small cap", content: strings.Repeat("example ", 100),
			options: workflowToolOptions{MaxResultTokens: 1},
		},
		{
			name: "token cap", content: strings.Repeat("example ", 100),
			options: workflowToolOptions{MaxResultTokens: 16},
		},
		{
			name: "large unbroken text", content: strings.Repeat("x", 65536),
			options: workflowToolOptions{MaxResultTokens: 16},
		},
		{
			name: "unicode byte cap", content: strings.Repeat("\u754c", 100),
			options: workflowToolOptions{MaxResultBytes: 17},
		},
		{
			name: "tiny byte cap", content: "example output",
			options: workflowToolOptions{MaxResultBytes: 2},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result, err := boundWorkflowToolContent(t.Context(), tc.content,
				tc.options)
			require.NoError(t, err)
			assert.True(t, utf8.ValidString(result))
			if tc.options.MaxResultBytes > 0 {
				assert.LessOrEqual(t, len(result), tc.options.MaxResultBytes)
			}

			if tc.options.MaxResultTokens > 0 {
				count, err := workflowContentTokens(elelem.DefaultTokenCounter(), result)
				require.NoError(t, err)
				assert.LessOrEqual(t, count, tc.options.MaxResultTokens)
			}
		})
	}
}
