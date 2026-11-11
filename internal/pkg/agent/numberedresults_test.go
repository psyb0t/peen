package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNumberedResultsKeepCompleteLinesWhenCut(t *testing.T) {
	t.Parallel()
	lines := []string{}
	for line := 183; line < 203; line++ {
		lines = append(lines, fmt.Sprintf("%6d\t%s\\n literal",
			line, strings.Repeat("word ", 25)))
	}

	lines = append(lines, "[lines 183-202 of 300; next offset 203]")
	encoded, err := json.Marshal(map[string]any{
		"content": strings.Join(lines, "\n"), "firstLine": 183,
		"totalLines": 300, "sha256": "whole-file-hash",
	})
	require.NoError(t, err)
	wrapped, err := markToolOutput(t.Context(), toolNameReadFile,
		string(encoded), 0)
	require.NoError(t, err)
	cut, err := cutToolResult(t.Context(), elelem.DefaultTokenCounter(),
		wrapped, 400)
	require.NoError(t, err)
	for _, output := range []string{cut, clipCompactionToolResult(wrapped, 1500)} {
		_, inner, _, ok := splitToolOutput(output)
		require.True(t, ok)
		fields, kept := numberedResult(inner)
		require.NotNil(t, fields)
		assert.Equal(t, `"whole-file-hash"`, string(fields["sha256"]))
		assert.Contains(t, kept, workflowToolTruncatedMarker)
		for _, line := range kept {
			if line != workflowToolTruncatedMarker {
				assert.Contains(t, lines, line)
			}
		}
	}
}
