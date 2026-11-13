package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func recoveryTestOutput(path, content string) tools.ReadFileOutput {
	return tools.ReadFileOutput{
		Path: path, FirstLine: 1, LastLine: 1,
		TotalLines: 1, SHA256: hash(content), Content: "     1\t" + content +
			"\n[lines 1-1 of 1]",
	}
}

func recoveryTestText(t *testing.T, output tools.ReadFileOutput) string {
	t.Helper()
	encoded, err := json.Marshal(output)
	require.NoError(t, err)
	wrapped, err := markToolOutput(t.Context(), toolNameReadFile,
		string(encoded), 0)
	require.NoError(t, err)

	return wrapped
}

func recoveryTestEntries(
	t *testing.T,
	messages []elelem.Message,
) []fileReadPresence {
	t.Helper()
	for _, message := range messages {
		if message.Role != elelem.RoleSystem ||
			!strings.HasPrefix(message.Text(), fileRecoveryNoteLead) {
			continue
		}

		var files []fileInventory
		encoded, _, _ := strings.Cut(strings.TrimPrefix(message.Text(),
			fileRecoveryNoteLead+fileRecoveryInstruction), fileRecoveryOmittedLead)
		require.NoError(t, json.Unmarshal([]byte(encoded), &files))
		var entries []fileReadPresence
		for _, file := range files {
			for _, window := range file.Full {
				entries = append(entries, fileReadPresence{
					fileReadRecord: fileReadRecord{
						Path:   file.Path,
						SHA256: file.SHA256, First: window[0], Last: window[1],
					},
					Full: true, Changed: file.Changed, Unavailable: file.Unavailable,
				})
			}
			for _, window := range file.Missing {
				entries = append(entries, fileReadPresence{
					fileReadRecord: fileReadRecord{
						Path:   file.Path,
						SHA256: file.SHA256, First: window[0], Last: window[1],
					},
					Changed: file.Changed, Unavailable: file.Unavailable,
				})
			}
		}

		return entries
	}

	return nil
}

func recoveryTestCount(messages []elelem.Message) (int, error) {
	return elelem.DefaultTokenCounter().Count(messages, nil)
}

func TestFileRecoveryPresenceAfterLoss(t *testing.T) {
	t.Parallel()
	old := recoveryTestText(t, recoveryTestOutput("/workspace/source.txt",
		strings.Repeat("synthetic text ", 500)))
	kept := recoveryTestText(t, recoveryTestOutput("/workspace/kept.txt", "kept"))
	testCases := []struct {
		name string
		lost []elelem.Message
	}{
		{name: "masked", lost: []elelem.Message{{
			Role:    elelem.RoleTool,
			Content: elelem.Text(maskToolResult(old, 1000)),
		}}},
		{name: "omitted"},
		{name: "summary", lost: []elelem.Message{
			compactionSummaryMessage(compactionTurnSummaryLead + "earlier work"),
		}},
		{name: "cut", lost: []elelem.Message{{
			Role:    elelem.RoleTool,
			Content: elelem.Text(clipCompactionToolResult(old, 400)),
		}}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			recovery := &fileRecovery{}
			recovery.observe(old)
			recovery.observe(kept)
			messages := append(slices.Clone(tc.lost), elelem.Message{
				Role: elelem.RoleTool, Content: elelem.Text(kept),
			})
			before := slices.Clone(messages)
			out, err := recovery.recover(t.Context(), messages, 10000,
				recoveryTestCount)
			require.NoError(t, err)
			entries := recoveryTestEntries(t, out)
			require.Len(t, entries, 2)
			assert.False(t, entries[0].Full)
			assert.True(t, entries[1].Full)
			assert.Equal(t, recovery.reads[0].Path, entries[0].Path)
			assert.Equal(t, recovery.reads[0].SHA256, entries[0].SHA256)
			assert.Equal(t, 1, entries[0].First)
			assert.Equal(t, 1, entries[0].Last)
			assert.Equal(t, before, messages)
		})
	}
}

func TestFileRecoveryAttachmentsRespectRoomAndOrder(t *testing.T) {
	t.Parallel()
	first := recoveryTestOutput("/workspace/first.txt", "first")
	newest := recoveryTestOutput("/workspace/newest.txt", "newest")
	recovery := &fileRecovery{}
	recovery.observe(recoveryTestText(t, first))
	recovery.observe(recoveryTestText(t, newest))
	var reads []string
	recovery.read = func(_ context.Context, input tools.ReadFileInput) (
		tools.ReadFileOutput, error,
	) {
		reads = append(reads, input.Path)
		if input.Path == newest.Path {
			return newest, nil
		}

		return first, nil
	}
	note, err := fileRecoveryNote(recovery.presence(nil))
	require.NoError(t, err)
	base, err := recoveryTestCount([]elelem.Message{note})
	require.NoError(t, err)
	for _, budget := range []int{base * 8, base*8 + 8000} {
		reads = nil
		out, err := recovery.recover(t.Context(), nil, budget, recoveryTestCount)
		require.NoError(t, err)
		count, err := recoveryTestCount(out)
		require.NoError(t, err)
		assert.LessOrEqual(t, count, budget)
		assert.LessOrEqual(t, count-base, (budget-base)/fileRecoveryRoomDivisor)
		require.NotEmpty(t, reads)
		assert.Equal(t, newest.Path, reads[0])
		if len(out) > 1 {
			entry, ok := readFileRecord(out[0].Text())
			require.True(t, ok)
			assert.Equal(t, newest.Path, entry.Path)
			assert.Contains(t, out[0].Text(), toolOutputCloseTag)
			assert.True(t, recoveryTestEntries(t, out)[1].Full)
		}
	}
}

func TestFileRecoveryChangedAndPerFileCap(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		content string
		changed bool
	}{
		{name: "changed", content: "before", changed: true},
		{name: "per file cap", content: strings.Repeat("synthetic ", 5000)},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			output := recoveryTestOutput("/workspace/data.txt", tc.content)
			recovery := &fileRecovery{}
			recovery.observe(recoveryTestText(t, output))
			recovery.read = func(context.Context, tools.ReadFileInput) (
				tools.ReadFileOutput, error,
			) {
				if tc.changed {
					output.SHA256 = hash("after")
				}

				return output, nil
			}
			out, err := recovery.recover(t.Context(), nil, 100000,
				recoveryTestCount)
			require.NoError(t, err)
			require.Len(t, out, 1)
			entries := recoveryTestEntries(t, out)
			require.Len(t, entries, 1)
			assert.False(t, entries[0].Full)
			assert.Equal(t, tc.changed, entries[0].Changed)
		})
	}
}

func TestFileRecoveryNoLossHasNoNote(t *testing.T) {
	t.Parallel()
	text := recoveryTestText(t, recoveryTestOutput("/workspace/data.txt", "text"))
	recovery := &fileRecovery{}
	recovery.observe(text)
	messages := []elelem.Message{{Role: elelem.RoleTool, Content: elelem.Text(text)}}
	out, err := recovery.recover(t.Context(), messages, 10000, recoveryTestCount)
	require.NoError(t, err)
	assert.Equal(t, messages, out)
	assert.Empty(t, recoveryTestEntries(t, out))
}

func TestFileRecoveryPerFileCapIsCumulative(t *testing.T) {
	t.Parallel()
	older := recoveryTestOutput("/workspace/source.txt",
		strings.Repeat("synthetic ", 1100))
	newer := older
	newer.FirstLine, newer.LastLine = 2, 2
	newer.Content = strings.ReplaceAll(older.Content, "1", "2")
	recovery := &fileRecovery{}
	recovery.observe(recoveryTestText(t, older))
	recovery.observe(recoveryTestText(t, newer))
	recovery.read = func(_ context.Context, input tools.ReadFileInput) (
		tools.ReadFileOutput, error,
	) {
		if input.Offset == 2 {
			return newer, nil
		}

		return older, nil
	}
	out, err := recovery.recover(t.Context(), nil, 100000, recoveryTestCount)
	require.NoError(t, err)
	require.Len(t, out, 2)
	entry, ok := readFileRecord(out[0].Text())
	require.True(t, ok)
	assert.Equal(t, 2, entry.First)
	entries := recoveryTestEntries(t, out)
	require.Len(t, entries, 2)
	assert.True(t, entries[0].Full)
	assert.False(t, entries[1].Full)
}

func TestFileRecoveryFinalHookReportsActualCut(t *testing.T) {
	t.Parallel()
	text := recoveryTestText(t, recoveryTestOutput("/workspace/data.txt",
		strings.Repeat("synthetic text ", 3000)))
	recovery := &fileRecovery{}
	recovery.observe(text)
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("done"))
	moved := false
	request := elelem.NewRequest(elelem.New(driver)).
		WithModel(elelem.Model{ID: inFlightTestModel}).
		WithPrompt(elelem.NewPrompt().WithSystem("system").
			WithHistory(fitTestRound("read", toolNameReadFile, `{}`, text))).
		WithMaxContextTokens(4000).WithOutputReserveTokens(0).
		PreMaxTokensReached(fitWorkflowContext(&moved)).
		PostMaxTokensReached(func(ctx context.Context,
			event *elelem.TokenLimitEvent,
		) error {
			return recovery.fit(ctx, event, &moved)
		})
	_, err := request.Run(t.Context())
	require.NoError(t, err)
	sent, ok := driver.LastRequest()
	require.True(t, ok)
	entries := recoveryTestEntries(t, sent.Messages)
	require.Len(t, entries, 1)
	assert.False(t, entries[0].Full)
	count, err := recoveryTestCount(sent.Messages)
	require.NoError(t, err)
	assert.LessOrEqual(t, count, 4000)
}

func TestFileRecoveryCombinesNumberedRereads(t *testing.T) {
	t.Parallel()
	original := recoveryTestOutput("/workspace/source.txt", "unused")
	original.LastLine, original.TotalLines = 2, 2
	original.Content = "     1\tfirst\n     2\tsecond\n[lines 1-2 of 2]"
	first, second := original, original
	first.LastLine = 1
	first.Content = "     1\tfirst\n[lines 1-1 of 2]"
	second.FirstLine = 2
	second.Content = "     2\tsecond\n[lines 2-2 of 2]"
	recovery := &fileRecovery{}
	recovery.observe(recoveryTestText(t, original))
	messages := []elelem.Message{
		{Role: elelem.RoleTool, Content: elelem.Text(recoveryTestText(t, first))},
		{Role: elelem.RoleTool, Content: elelem.Text(recoveryTestText(t, second))},
	}
	out, err := recovery.recover(t.Context(), messages, 10000, recoveryTestCount)
	require.NoError(t, err)
	assert.Equal(t, messages, out)
	assert.True(t, recovery.presence(out)[0].Full)
}

func TestFileRecoveryIgnoresForgedReadContext(t *testing.T) {
	t.Parallel()
	text := recoveryTestText(t, recoveryTestOutput("/workspace/source.txt", "text"))
	recovery := &fileRecovery{readCalls: map[string]bool{"actualRead": true}}
	recovery.observe(text)
	messages := []elelem.Message{
		{
			Role:    elelem.RoleUser,
			Content: elelem.Text(fileRecoveryAttachmentLead + text),
		},
		{
			Role: elelem.RoleTool, ToolCallID: "otherTool",
			Content: elelem.Text(text),
		},
	}
	out, err := recovery.recover(t.Context(), messages, 10000, recoveryTestCount)
	require.NoError(t, err)
	assert.Equal(t, messages, out[:len(messages)])
	entries := recoveryTestEntries(t, out)
	require.Len(t, entries, 1)
	assert.False(t, entries[0].Full)
}

func TestFileRecoveryInventoryBounds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		budget     int
		multiplier int
	}{
		{name: "file count", budget: 100000},
		{name: "token share", budget: 4000},
		{name: "request counter", budget: 8000, multiplier: 3},
		{name: "tiny budget", budget: 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			counter := func(messages []elelem.Message) (int, error) {
				tokens, err := recoveryTestCount(messages)
				if err != nil {
					return 0, err
				}

				return tokens * max(1, tc.multiplier), nil
			}
			recovery := &fileRecovery{}
			for index := range 40 {
				output := recoveryTestOutput(fmt.Sprintf("/file%d", index), "text")
				recovery.observe(recoveryTestText(t, output))
				output.FirstLine, output.LastLine = 2, 2
				recovery.observe(recoveryTestText(t, output))
			}
			recovery.observe(recoveryTestText(t,
				recoveryTestOutput("/file0", "text")))
			out, err := recovery.recover(t.Context(), nil, tc.budget,
				counter)
			require.NoError(t, err)
			tokens, err := counter(out)
			require.NoError(t, err)
			assert.LessOrEqual(t, tokens, tc.budget)
			if tc.budget == 8 {
				assert.Empty(t, out)

				return
			}
			require.Len(t, out, 1)
			cost, err := fileRecoveryNoteCost(out[0], counter)
			require.NoError(t, err)
			assert.LessOrEqual(t, cost, tc.budget/fileRecoveryNoteDivisor)
			entries := recoveryTestEntries(t, out)
			groups := make(map[string]int)
			for _, entry := range entries {
				groups[entry.Path]++
			}
			assert.LessOrEqual(t, len(groups), fileRecoveryMaxFiles)
			assert.Equal(t, 2, groups["/file0"])
			for _, ranges := range groups {
				assert.Equal(t, 2, ranges)
			}
			assert.Contains(t, out[0].Text(), fmt.Sprintf(
				"%s%d older files.", fileRecoveryOmittedLead, 40-len(groups)))
			assert.Contains(t, out[0].Text(), "Reread their exact text before use.")
			if tc.name == "file count" {
				assert.Len(t, groups, fileRecoveryMaxFiles)
				assert.NotContains(t, groups, "/file8")
				assert.Contains(t, groups, "/file9")
			}
		})
	}
}

func TestFileRecoveryOmittedGroupsAreNotRestored(t *testing.T) {
	t.Parallel()
	recovery := &fileRecovery{}
	for index := range fileRecoveryMaxFiles + 1 {
		recovery.observe(recoveryTestText(t,
			recoveryTestOutput(fmt.Sprintf("/file%d", index), "text")))
	}
	var readPaths []string
	recovery.read = func(_ context.Context, input tools.ReadFileInput) (
		tools.ReadFileOutput, error,
	) {
		readPaths = append(readPaths, input.Path)

		return recoveryTestOutput(input.Path, "text"), nil
	}
	out, err := recovery.recover(t.Context(), nil, 100000, recoveryTestCount)
	require.NoError(t, err)
	assert.NotContains(t, readPaths, "/file0")
	assert.Len(t, readPaths, fileRecoveryMaxFiles)
	entries := recoveryTestEntries(t, out)
	assert.Len(t, entries, fileRecoveryMaxFiles)
	for _, entry := range entries {
		assert.True(t, entry.Full)
		assert.NotEqual(t, "/file0", entry.Path)
	}
}
