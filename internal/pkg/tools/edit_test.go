package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testFileMode               = 0o644
	testFileModeReadWriteOwner = 0o600
	testSmallDiffBytes         = 10
	testTinyWriteBytes         = 5
)

func newEditTestExecutor(t *testing.T, limits Limits) (*Executor, string) {
	t.Helper()

	dir := t.TempDir()

	exec, err := NewExecutor(Options{Workspace: dir, Limits: limits})
	require.NoError(t, err)

	return exec, dir
}

func writeEditFixture(t *testing.T, dir, name, content string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), testFileMode))

	return path
}

func observeEditFixture(exec *Executor, path string) {
	exec.observe(path)
}

func readFixtureContent(t *testing.T, path string) []byte {
	t.Helper()

	content, err := os.ReadFile(path)
	require.NoError(t, err)

	return content
}

func TestExecutor_EditFile_SingleEdit(t *testing.T) {
	t.Parallel()

	exec, dir := newEditTestExecutor(t, Limits{})
	path := writeEditFixture(t, dir, "single.txt", "hello world\n")
	observeEditFixture(exec, path)

	output, err := exec.EditFile(context.Background(), EditFileInput{
		Path: path,
		Edits: []TextEdit{
			{Old: "world", New: "there"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, path, output.Path)
	assert.Equal(t, 1, output.Applied)
	assert.False(t, output.DiffTruncated)

	content := readFixtureContent(t, path)
	assert.Equal(t, "hello there\n", string(content))
	assert.Equal(t, hashBytes(content), output.SHA256)
}

func TestExecutor_EditFile_DiffContent(t *testing.T) {
	t.Parallel()

	exec, dir := newEditTestExecutor(t, Limits{})
	path := writeEditFixture(t, dir, "diff.txt", "hello world\n")
	observeEditFixture(exec, path)

	output, err := exec.EditFile(context.Background(), EditFileInput{
		Path: path,
		Edits: []TextEdit{
			{Old: "world", New: "there"},
		},
	})
	require.NoError(t, err)

	expected := "--- " + path + "\n" +
		"+++ " + path + "\n" +
		"@@ -1,1 +1,1 @@\n" +
		"-hello world\n" +
		"+hello there\n"

	assert.Equal(t, expected, output.Diff)
	assert.Contains(t, output.Diff, "--- "+path)
	assert.Contains(t, output.Diff, "+++ "+path)
	assert.Contains(t, output.Diff, "@@ -1,1 +1,1 @@")
	assert.Contains(t, output.Diff, "-hello world")
	assert.Contains(t, output.Diff, "+hello there")
}

func TestExecutor_EditFile_SeveralNonOverlappingEdits(t *testing.T) {
	t.Parallel()

	exec, dir := newEditTestExecutor(t, Limits{})
	path := writeEditFixture(t, dir, "several.txt", "one two three\n")
	observeEditFixture(exec, path)

	output, err := exec.EditFile(context.Background(), EditFileInput{
		Path: path,
		Edits: []TextEdit{
			{Old: "three", New: "3"},
			{Old: "one", New: "1"},
			{Old: "two", New: "2"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 3, output.Applied)

	content := readFixtureContent(t, path)
	assert.Equal(t, "1 2 3\n", string(content))
}

func TestExecutor_EditFile_DeletesText(t *testing.T) {
	t.Parallel()

	exec, dir := newEditTestExecutor(t, Limits{})
	path := writeEditFixture(t, dir, "delete.txt", "keep DROP keep\n")
	observeEditFixture(exec, path)

	output, err := exec.EditFile(context.Background(), EditFileInput{
		Path: path,
		Edits: []TextEdit{
			{Old: "DROP ", New: ""},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, output.Applied)

	content := readFixtureContent(t, path)
	assert.Equal(t, "keep keep\n", string(content))
}

func TestExecutor_EditFile_TouchingRangesSucceed(t *testing.T) {
	t.Parallel()

	exec, dir := newEditTestExecutor(t, Limits{})
	path := writeEditFixture(t, dir, "touching.txt", "abcdef")
	observeEditFixture(exec, path)

	output, err := exec.EditFile(context.Background(), EditFileInput{
		Path: path,
		Edits: []TextEdit{
			{Old: "abc", New: "123"},
			{Old: "def", New: "456"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 2, output.Applied)

	content := readFixtureContent(t, path)
	assert.Equal(t, "123456", string(content))
}

func TestExecutor_EditFile_OverlappingEditsRejected(t *testing.T) {
	t.Parallel()

	exec, dir := newEditTestExecutor(t, Limits{})
	path := writeEditFixture(t, dir, "overlap.txt", "abcdef")
	observeEditFixture(exec, path)

	before := readFixtureContent(t, path)

	_, err := exec.EditFile(context.Background(), EditFileInput{
		Path: path,
		Edits: []TextEdit{
			{Old: "abcd", New: "1234"},
			{Old: "cdef", New: "5678"},
		},
	})
	require.ErrorIs(t, err, ErrOverlappingEdits)

	after := readFixtureContent(t, path)
	assert.Equal(t, before, after)
	assertNoTemporaryFiles(t, dir)
}

func TestExecutor_EditFile_MatchNotFound(t *testing.T) {
	t.Parallel()

	exec, dir := newEditTestExecutor(t, Limits{})
	path := writeEditFixture(t, dir, "notfound.txt", "abc\n")
	observeEditFixture(exec, path)

	_, err := exec.EditFile(context.Background(), EditFileInput{
		Path: path,
		Edits: []TextEdit{
			{Old: "zzz", New: "yyy"},
		},
	})
	require.ErrorIs(t, err, ErrMatchNotFound)
}

func TestExecutor_EditFile_MatchNotUnique(t *testing.T) {
	t.Parallel()

	exec, dir := newEditTestExecutor(t, Limits{})
	path := writeEditFixture(t, dir, "notunique.txt", "abc abc\n")
	observeEditFixture(exec, path)

	before := readFixtureContent(t, path)

	_, err := exec.EditFile(context.Background(), EditFileInput{
		Path: path,
		Edits: []TextEdit{
			{Old: "abc", New: "xyz"},
		},
	})
	require.ErrorIs(t, err, ErrMatchNotUnique)

	after := readFixtureContent(t, path)
	assert.Equal(t, before, after)
	assertNoTemporaryFiles(t, dir)
}

func TestExecutor_EditFile_UnobservedPath(t *testing.T) {
	t.Parallel()

	exec, dir := newEditTestExecutor(t, Limits{})
	path := writeEditFixture(t, dir, "unobserved.txt", "abc\n")

	_, err := exec.EditFile(context.Background(), EditFileInput{
		Path: path,
		Edits: []TextEdit{
			{Old: "abc", New: "xyz"},
		},
	})
	require.ErrorIs(t, err, ErrNotObserved)
}

func TestExecutor_EditFile_BinaryContentRejected(t *testing.T) {
	t.Parallel()

	exec, dir := newEditTestExecutor(t, Limits{})
	path := writeEditFixture(t, dir, "binary.bin", "abc\x00def")
	observeEditFixture(exec, path)

	_, err := exec.EditFile(context.Background(), EditFileInput{
		Path: path,
		Edits: []TextEdit{
			{Old: "abc", New: "xyz"},
		},
	})
	require.ErrorIs(t, err, ErrBinaryContent)
}

func TestExecutor_EditFile_WriteBoundExceeded(t *testing.T) {
	t.Parallel()

	exec, dir := newEditTestExecutor(t, Limits{MaxWriteBytes: testTinyWriteBytes})
	path := writeEditFixture(t, dir, "toolarge.txt", "this content is too large\n")
	observeEditFixture(exec, path)

	_, err := exec.EditFile(context.Background(), EditFileInput{
		Path: path,
		Edits: []TextEdit{
			{Old: "content", New: "text"},
		},
	})
	require.ErrorIs(t, err, ErrLimitExceeded)
}

func TestExecutor_EditFile_ModePreserved(t *testing.T) {
	t.Parallel()

	exec, dir := newEditTestExecutor(t, Limits{})
	path := writeEditFixture(t, dir, "mode.txt", "abc\n")
	require.NoError(t, os.Chmod(path, testFileModeReadWriteOwner))
	observeEditFixture(exec, path)

	_, err := exec.EditFile(context.Background(), EditFileInput{
		Path: path,
		Edits: []TextEdit{
			{Old: "abc", New: "xyz"},
		},
	})
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(testFileModeReadWriteOwner), info.Mode().Perm())
}

func TestExecutor_EditFile_CRLFContent(t *testing.T) {
	t.Parallel()

	exec, dir := newEditTestExecutor(t, Limits{})
	path := writeEditFixture(t, dir, "crlf.txt", "line1\r\nline2\r\n")
	observeEditFixture(exec, path)

	output, err := exec.EditFile(context.Background(), EditFileInput{
		Path: path,
		Edits: []TextEdit{
			{Old: "line1", New: "LINE1"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, output.Applied)

	content := readFixtureContent(t, path)
	assert.Equal(t, "LINE1\r\nline2\r\n", string(content))
}

func TestExecutor_EditFile_MultiByteUnicode(t *testing.T) {
	t.Parallel()

	exec, dir := newEditTestExecutor(t, Limits{})
	path := writeEditFixture(t, dir, "unicode.txt", "café \U0001F600 world\n")
	observeEditFixture(exec, path)

	output, err := exec.EditFile(context.Background(), EditFileInput{
		Path: path,
		Edits: []TextEdit{
			{Old: "\U0001F600", New: "\U0001F601"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, output.Applied)

	content := readFixtureContent(t, path)
	assert.Equal(t, "café \U0001F601 world\n", string(content))
}

func TestExecutor_EditFile_NoTrailingNewline(t *testing.T) {
	t.Parallel()

	exec, dir := newEditTestExecutor(t, Limits{})
	path := writeEditFixture(t, dir, "notrailing.txt", "abc")
	observeEditFixture(exec, path)

	output, err := exec.EditFile(context.Background(), EditFileInput{
		Path: path,
		Edits: []TextEdit{
			{Old: "abc", New: "xyz"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, output.Applied)

	content := readFixtureContent(t, path)
	assert.Equal(t, "xyz", string(content))
}

func TestExecutor_EditFile_DiffTruncated(t *testing.T) {
	t.Parallel()

	exec, dir := newEditTestExecutor(t, Limits{MaxDiffBytes: testSmallDiffBytes})
	original := strings.Repeat("filler line\n", 50) + "target\n"
	path := writeEditFixture(t, dir, "truncated.txt", original)
	observeEditFixture(exec, path)

	output, err := exec.EditFile(context.Background(), EditFileInput{
		Path: path,
		Edits: []TextEdit{
			{Old: "target", New: "replacement"},
		},
	})
	require.NoError(t, err)
	assert.True(t, output.DiffTruncated)
	assert.Contains(t, output.Diff, diffTruncatedTag)
}

func TestExecutor_EditFile_CancelledContext(t *testing.T) {
	t.Parallel()

	exec, dir := newEditTestExecutor(t, Limits{})
	path := writeEditFixture(t, dir, "cancelled.txt", "abc\n")
	observeEditFixture(exec, path)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := exec.EditFile(ctx, EditFileInput{
		Path: path,
		Edits: []TextEdit{
			{Old: "abc", New: "xyz"},
		},
	})
	require.ErrorIs(t, err, context.Canceled)
}

func TestExecutor_EditFile_SHA256MatchesDisk(t *testing.T) {
	t.Parallel()

	exec, dir := newEditTestExecutor(t, Limits{})
	path := writeEditFixture(t, dir, "hash.txt", "abc\n")
	observeEditFixture(exec, path)

	output, err := exec.EditFile(context.Background(), EditFileInput{
		Path: path,
		Edits: []TextEdit{
			{Old: "abc", New: "xyz"},
		},
	})
	require.NoError(t, err)

	content := readFixtureContent(t, path)
	assert.Equal(t, hashBytes(content), output.SHA256)
}

func TestExecutor_EditFile_Validation(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		input   EditFileInput
		wantErr error
	}{
		{
			name:    "empty path",
			input:   EditFileInput{Path: "", Edits: []TextEdit{{Old: "a", New: "b"}}},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name:    "no edits",
			input:   EditFileInput{Path: "placeholder", Edits: nil},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "empty old text",
			input: EditFileInput{
				Path:  "placeholder",
				Edits: []TextEdit{{Old: "", New: "b"}},
			},
			wantErr: commerr.ErrValidationFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			exec, dir := newEditTestExecutor(t, Limits{})
			path := writeEditFixture(t, dir, "placeholder", "abc\n")
			observeEditFixture(exec, path)

			input := tc.input
			if input.Path == "placeholder" {
				input.Path = path
			}

			_, err := exec.EditFile(context.Background(), input)
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestExecutor_EditFile_MaxEditsExceeded(t *testing.T) {
	t.Parallel()

	exec, dir := newEditTestExecutor(t, Limits{MaxEdits: 1})
	path := writeEditFixture(t, dir, "maxedits.txt", "abc def\n")
	observeEditFixture(exec, path)

	_, err := exec.EditFile(context.Background(), EditFileInput{
		Path: path,
		Edits: []TextEdit{
			{Old: "abc", New: "xyz"},
			{Old: "def", New: "uvw"},
		},
	})
	require.ErrorIs(t, err, ErrLimitExceeded)
}

func assertNoTemporaryFiles(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	for _, entry := range entries {
		assert.False(
			t,
			strings.HasPrefix(entry.Name(), temporaryFilePrefix),
			"unexpected temporary file left behind: %s",
			entry.Name(),
		)
	}
}
