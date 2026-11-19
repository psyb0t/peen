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

// readFileResult builds a readFile result the way the tool returns a whole
// window: numbered lines, its own range line, and metadata saying it is
// complete.
func readFileResult(t *testing.T, lines int) string {
	t.Helper()

	numbered := make([]string, 0, lines+1)
	for line := 1; line <= lines; line++ {
		numbered = append(numbered, fmt.Sprintf("%6d\tfunc f%d[T any](a, b T) "+
			"bool { return a < b && b > a }", line, line))
	}

	numbered = append(numbered, fmt.Sprintf("[lines 1-%d of %d]", lines, lines))

	encoded, err := json.Marshal(map[string]any{
		"path": "/app/big.go", "content": strings.Join(numbered, "\n"),
		"firstLine": 1, "lastLine": lines, "totalLines": lines,
		"nextOffset": 0, "truncated": false, "sha256": "whole-file-hash",
	})
	require.NoError(t, err)

	return string(encoded)
}

func TestCappedReadFileResultReportsWhereToContinue(t *testing.T) {
	t.Parallel()

	const maxTokens = 2000

	marked, err := markToolOutput(t.Context(), toolNameReadFile,
		readFileResult(t, 2000), maxTokens)
	require.NoError(t, err)

	count, err := workflowContentTokens(elelem.DefaultTokenCounter(), marked)
	require.NoError(t, err)
	assert.LessOrEqual(t, count, maxTokens)

	_, inner, _, ok := splitToolOutput(marked)
	require.True(t, ok)

	var result struct {
		Content    string `json:"content"`
		FirstLine  int    `json:"firstLine"`
		LastLine   int    `json:"lastLine"`
		TotalLines int    `json:"totalLines"`
		NextOffset int    `json:"nextOffset"`
		Truncated  bool   `json:"truncated"`
		SHA256     string `json:"sha256"`
	}
	require.NoError(t, json.Unmarshal([]byte(inner), &result), inner)

	lines := strings.Split(result.Content, "\n")
	lastKept, ok := numberedLineNumber(lines[len(lines)-2])
	require.True(t, ok)

	assert.True(t, result.Truncated)
	assert.Equal(t, 1, result.FirstLine)
	assert.Equal(t, 2000, result.TotalLines)
	assert.Equal(t, lastKept, result.LastLine)
	assert.Less(t, result.LastLine, 2000)
	assert.Equal(t, result.LastLine+1, result.NextOffset)
	assert.Equal(t, "whole-file-hash", result.SHA256)
	assert.Equal(t, fmt.Sprintf("[lines 1-%d of 2000; next offset %d; the "+
		"rest of this window did not fit the tool result size limit]",
		result.LastLine, result.NextOffset), lines[len(lines)-1])
	assert.Contains(t, result.Content, "[T any](a, b T) bool { return a < b && b > a }")
	assert.NotContains(t, inner, `\`+"u003c")
	assert.NotContains(t, inner, `\`+"u0026")
}

func TestCappedCommandResultKeepsNewestOutputAndState(t *testing.T) {
	t.Parallel()

	const maxTokens = 1500

	stdout := make([]string, 0, 4000)
	for line := 1; line <= 4000; line++ {
		stdout = append(stdout, fmt.Sprintf("progress line %d of the build", line))
	}

	stdout = append(stdout, "FAIL: TestSomething expected <nil>")

	encoded, err := json.Marshal(map[string]any{
		"jobId": "job", "state": "exited", "exitCode": 1,
		"stdoutSkippedLines": 0, "stderr": "warning: x && y",
		"stdout": strings.Join(stdout, "\n"),
	})
	require.NoError(t, err)

	marked, err := markToolOutput(t.Context(), toolNameRunCommand,
		string(encoded), maxTokens)
	require.NoError(t, err)

	count, err := workflowContentTokens(elelem.DefaultTokenCounter(), marked)
	require.NoError(t, err)
	assert.LessOrEqual(t, count, maxTokens)

	_, inner, _, ok := splitToolOutput(marked)
	require.True(t, ok)

	var result struct {
		State     string `json:"state"`
		ExitCode  int    `json:"exitCode"`
		Stdout    string `json:"stdout"`
		Stderr    string `json:"stderr"`
		ResultCut bool   `json:"resultCut"`
	}
	require.NoError(t, json.Unmarshal([]byte(inner), &result), inner)

	assert.Equal(t, "exited", result.State)
	assert.Equal(t, 1, result.ExitCode)
	assert.True(t, result.ResultCut)
	assert.Equal(t, "warning: x && y", result.Stderr)
	assert.True(t, strings.HasSuffix(result.Stdout,
		"FAIL: TestSomething expected <nil>"))
	assert.True(t, strings.HasPrefix(result.Stdout, "["), result.Stdout[:80])
	assert.Contains(t, result.Stdout, "earlier bytes left out to fit the "+
		"tool result size limit]\nprogress line")
}

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
	outputs := map[string]string{
		"cap": cut, "compaction": clipCompactionToolResult(wrapped, 1500),
	}
	for name, output := range outputs {
		_, inner, _, ok := splitToolOutput(output)
		require.True(t, ok, name)
		fields, kept := numberedResult(inner)
		require.NotNil(t, fields, name)
		assert.Equal(t, `"whole-file-hash"`, string(fields["sha256"]), name)

		marker := workflowToolTruncatedMarker
		if name == "cap" {
			marker = kept[len(kept)-1]
			assert.True(t, strings.HasPrefix(marker, "[lines 183-"), marker)
			assert.Equal(t, "true", string(fields["truncated"]))
		}

		assert.Contains(t, kept, marker, name)
		for _, line := range kept {
			if line != marker {
				assert.Contains(t, lines, line, name)
			}
		}
	}
}
