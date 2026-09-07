package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newSearchTextExecutor(t *testing.T, workspace string) *Executor {
	t.Helper()

	exec, err := NewExecutor(Options{Workspace: workspace})
	require.NoError(t, err)

	return exec
}

func TestSearchText_Success(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "a.txt"),
		[]byte("hello world\nfoo bar\nhello again\n"),
		0o600,
	))

	exec := newSearchTextExecutor(t, workspace)

	output, err := exec.SearchText(context.Background(), SearchTextInput{
		Pattern: "hello",
	})
	require.NoError(t, err)
	assert.False(t, output.Truncated)
	require.Len(t, output.Matches, 2)
	assert.Equal(t, 1, output.Matches[0].Line)
	assert.Equal(t, "hello world", output.Matches[0].Text)
	assert.Equal(t, 3, output.Matches[1].Line)
	assert.True(t, exec.Observed(filepath.Join(workspace, "a.txt")))
}

func TestSearchText_RegexAndLiteral(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "a.txt"),
		[]byte("num 42\nnum abc\n"),
		0o600,
	))

	exec := newSearchTextExecutor(t, workspace)

	literal, err := exec.SearchText(context.Background(), SearchTextInput{
		Pattern: "42",
	})
	require.NoError(t, err)
	require.Len(t, literal.Matches, 1)

	regex, err := exec.SearchText(context.Background(), SearchTextInput{
		Pattern: `num \d+`,
		Regex:   true,
	})
	require.NoError(t, err)
	require.Len(t, regex.Matches, 1)
	assert.Equal(t, "num 42", regex.Matches[0].Text)
}

func TestSearchText_BadRegexCompile(t *testing.T) {
	t.Parallel()

	exec := newSearchTextExecutor(t, t.TempDir())

	_, err := exec.SearchText(context.Background(), SearchTextInput{
		Pattern: "(unterminated",
		Regex:   true,
	})
	require.Error(t, err)
}

func TestSearchText_EmptyPattern(t *testing.T) {
	t.Parallel()

	exec := newSearchTextExecutor(t, t.TempDir())

	_, err := exec.SearchText(context.Background(), SearchTextInput{
		Pattern: "",
	})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

func TestSearchText_IncludeGlobFilter(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "a.go"), []byte("needle"), 0o600,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "b.txt"), []byte("needle"), 0o600,
	))

	exec := newSearchTextExecutor(t, workspace)

	output, err := exec.SearchText(context.Background(), SearchTextInput{
		Pattern: "needle",
		Include: []string{"*.go"},
	})
	require.NoError(t, err)
	require.Len(t, output.Matches, 1)
	assert.Equal(t, filepath.Join(workspace, "a.go"), output.Matches[0].Path)
}

func TestSearchText_RelativeAbsoluteAndParentPaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	other := filepath.Join(root, "other")
	require.NoError(t, os.Mkdir(workspace, 0o750))
	require.NoError(t, os.Mkdir(other, 0o750))
	require.NoError(t, os.WriteFile(
		filepath.Join(other, "f.txt"), []byte("needle"), 0o600,
	))

	exec := newSearchTextExecutor(t, workspace)

	testCases := []struct {
		name string
		path string
	}{
		{name: "absolute", path: other},
		{name: "parent traversing", path: "../other"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			output, err := exec.SearchText(
				context.Background(),
				SearchTextInput{Path: tc.path, Pattern: "needle"},
			)
			require.NoError(t, err)
			require.Len(t, output.Matches, 1)
		})
	}
}

func TestSearchText_HomeRelativePath(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	require.NoError(t, os.WriteFile(
		filepath.Join(home, "f.txt"), []byte("needle"), 0o600,
	))

	exec := newSearchTextExecutor(t, workspace)

	output, err := exec.SearchText(context.Background(), SearchTextInput{
		Path:    "~/f.txt",
		Pattern: "needle",
	})
	require.NoError(t, err)
	require.Len(t, output.Matches, 1)
}

func TestSearchText_SymlinkNotDescended(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	realDir := filepath.Join(workspace, "real")
	require.NoError(t, os.Mkdir(realDir, 0o750))
	require.NoError(t, os.WriteFile(
		filepath.Join(realDir, "inner.txt"), []byte("needle"), 0o600,
	))
	require.NoError(t, os.Symlink(realDir, filepath.Join(workspace, "link")))

	exec := newSearchTextExecutor(t, workspace)

	output, err := exec.SearchText(context.Background(), SearchTextInput{
		Pattern: "needle",
	})
	require.NoError(t, err)
	require.Len(t, output.Matches, 1)
	assert.Equal(t,
		filepath.Join(realDir, "inner.txt"), output.Matches[0].Path,
	)
}

func TestSearchText_MissingPath(t *testing.T) {
	t.Parallel()

	exec := newSearchTextExecutor(t, t.TempDir())

	_, err := exec.SearchText(context.Background(), SearchTextInput{
		Path:    "does-not-exist",
		Pattern: "needle",
	})
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

func TestSearchText_UnreadableDirectoryTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not enforced the same way on windows")
	}

	t.Parallel()

	workspace := t.TempDir()
	locked := filepath.Join(workspace, "locked")
	require.NoError(t, os.Mkdir(locked, 0o750))
	require.NoError(t, os.Chmod(locked, 0o000))

	t.Cleanup(func() {
		_ = os.Chmod(locked, 0o700) //nolint:gosec // needs the exec bit restored so t.TempDir() cleanup can traverse and remove the locked directory
	})

	exec := newSearchTextExecutor(t, workspace)

	_, err := exec.SearchText(context.Background(), SearchTextInput{
		Path:    locked,
		Pattern: "needle",
	})
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
}

func TestSearchText_UnreadableFileIsSkipped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not enforced the same way on windows")
	}

	t.Parallel()

	workspace := t.TempDir()
	locked := filepath.Join(workspace, "locked.txt")
	require.NoError(t, os.WriteFile(locked, []byte("needle"), 0o600))
	require.NoError(t, os.Chmod(locked, 0o000))

	t.Cleanup(func() {
		_ = os.Chmod(locked, 0o600)
	})

	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "visible.txt"), []byte("needle"), 0o600,
	))

	exec := newSearchTextExecutor(t, workspace)

	output, err := exec.SearchText(context.Background(), SearchTextInput{
		Pattern: "needle",
	})
	require.NoError(t, err)
	require.Len(t, output.Matches, 1)
	assert.Equal(t,
		filepath.Join(workspace, "visible.txt"), output.Matches[0].Path,
	)
}

func TestSearchText_BinaryFileIsSkipped(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "bin.dat"),
		[]byte("needle\x00binary"),
		0o600,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "text.txt"),
		[]byte("needle text"),
		0o600,
	))

	exec := newSearchTextExecutor(t, workspace)

	output, err := exec.SearchText(context.Background(), SearchTextInput{
		Pattern: "needle",
	})
	require.NoError(t, err)
	require.Len(t, output.Matches, 1)
	assert.Equal(t,
		filepath.Join(workspace, "text.txt"), output.Matches[0].Path,
	)
}

func TestSearchText_EmptyFileNoMatches(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "empty.txt"), []byte(""), 0o600,
	))

	exec := newSearchTextExecutor(t, workspace)

	output, err := exec.SearchText(context.Background(), SearchTextInput{
		Pattern: "needle",
	})
	require.NoError(t, err)
	assert.Empty(t, output.Matches)
}

func TestSearchText_FileTooLargeIsSkipped(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "big.txt"),
		[]byte("needle needle needle"),
		0o600,
	))

	exec, err := NewExecutor(Options{
		Workspace: workspace,
		Limits:    Limits{MaxSearchFileBytes: 4},
	})
	require.NoError(t, err)

	output, err := exec.SearchText(context.Background(), SearchTextInput{
		Pattern: "needle",
	})
	require.NoError(t, err)
	assert.Empty(t, output.Matches)
}

func TestSearchText_NoTrailingNewlineLastLineMatches(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "a.txt"), []byte("first\nneedle"), 0o600,
	))

	exec := newSearchTextExecutor(t, workspace)

	output, err := exec.SearchText(context.Background(), SearchTextInput{
		Pattern: "needle",
	})
	require.NoError(t, err)
	require.Len(t, output.Matches, 1)
	assert.Equal(t, 2, output.Matches[0].Line)
	assert.Equal(t, "needle", output.Matches[0].Text)
}

func TestSearchText_CRLFLineEndings(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "a.txt"),
		[]byte("first\r\nneedle here\r\n"),
		0o600,
	))

	exec := newSearchTextExecutor(t, workspace)

	output, err := exec.SearchText(context.Background(), SearchTextInput{
		Pattern: "needle",
	})
	require.NoError(t, err)
	require.Len(t, output.Matches, 1)
	assert.Equal(t, 2, output.Matches[0].Line)
}

func TestSearchText_MultiByteUnicode(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "a.txt"),
		[]byte("café 日本語 needle \U0001F600\n"), //nolint:gosmopolitan // non-ASCII text is the point of this test
		0o600,
	))

	exec := newSearchTextExecutor(t, workspace)

	output, err := exec.SearchText(context.Background(), SearchTextInput{
		Pattern: "needle",
	})
	require.NoError(t, err)
	require.Len(t, output.Matches, 1)
	assert.Contains(t, output.Matches[0].Text, "日本語") //nolint:gosmopolitan // non-ASCII text is the point of this test
}

func TestSearchText_MaxMatchesTruncates(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "a.txt"),
		[]byte("needle\nneedle\nneedle\nneedle\n"),
		0o600,
	))

	exec, err := NewExecutor(Options{
		Workspace: workspace,
		Limits:    Limits{MaxSearchMatches: 2},
	})
	require.NoError(t, err)

	output, err := exec.SearchText(context.Background(), SearchTextInput{
		Pattern: "needle",
	})
	require.NoError(t, err)
	assert.True(t, output.Truncated)
	assert.Len(t, output.Matches, 2)
}

func TestSearchText_MaxMatchesClampedToLimit(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "a.txt"),
		[]byte("needle\nneedle\nneedle\n"),
		0o600,
	))

	exec, err := NewExecutor(Options{
		Workspace: workspace,
		Limits:    Limits{MaxSearchMatches: 2},
	})
	require.NoError(t, err)

	output, err := exec.SearchText(context.Background(), SearchTextInput{
		Pattern:    "needle",
		MaxMatches: 100,
	})
	require.NoError(t, err)
	assert.True(t, output.Truncated)
	assert.Len(t, output.Matches, 2)
}

func TestSearchText_StableOrdering(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "zeta.txt"), []byte("needle\nneedle\n"), 0o600,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "alpha.txt"), []byte("needle\n"), 0o600,
	))

	exec := newSearchTextExecutor(t, workspace)

	output, err := exec.SearchText(context.Background(), SearchTextInput{
		Pattern: "needle",
	})
	require.NoError(t, err)
	require.Len(t, output.Matches, 3)
	assert.Equal(t, filepath.Join(workspace, "alpha.txt"), output.Matches[0].Path)
	assert.Equal(t, filepath.Join(workspace, "zeta.txt"), output.Matches[1].Path)
	assert.Equal(t, 1, output.Matches[1].Line)
	assert.Equal(t, filepath.Join(workspace, "zeta.txt"), output.Matches[2].Path)
	assert.Equal(t, 2, output.Matches[2].Line)
}

func TestSearchText_CancelledContext(t *testing.T) {
	t.Parallel()

	exec := newSearchTextExecutor(t, t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := exec.SearchText(ctx, SearchTextInput{Pattern: "needle"})
	require.ErrorIs(t, err, context.Canceled)
}
