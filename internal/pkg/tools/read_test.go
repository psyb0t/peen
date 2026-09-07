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

func newReadFileExecutor(t *testing.T, workspace string) *Executor {
	t.Helper()

	exec, err := NewExecutor(Options{Workspace: workspace})
	require.NoError(t, err)

	return exec
}

func TestReadFile_Success(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	content := "line1\nline2\nline3\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "a.txt"), []byte(content), 0o600,
	))

	exec := newReadFileExecutor(t, workspace)

	output, err := exec.ReadFile(context.Background(), ReadFileInput{
		Path: "a.txt",
	})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(workspace, "a.txt"), output.Path)
	assert.Equal(t, content, output.Content)
	assert.Equal(t, 1, output.FirstLine)
	assert.Equal(t, 3, output.LastLine)
	assert.Equal(t, 3, output.TotalLines)
	assert.Equal(t, 0, output.NextOffset)
	assert.False(t, output.Truncated)
	assert.Equal(t, hashBytes([]byte(content)), output.SHA256)
	assert.True(t, exec.Observed(filepath.Join(workspace, "a.txt")))
}

func TestReadFile_RelativeAbsoluteAndParentPaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	other := filepath.Join(root, "other")
	require.NoError(t, os.Mkdir(workspace, 0o750))
	require.NoError(t, os.Mkdir(other, 0o750))
	otherFile := filepath.Join(other, "f.txt")
	require.NoError(t, os.WriteFile(otherFile, []byte("x"), 0o600))

	exec := newReadFileExecutor(t, workspace)

	testCases := []struct {
		name string
		path string
	}{
		{name: "absolute", path: otherFile},
		{name: "parent traversing", path: "../other/f.txt"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			output, err := exec.ReadFile(
				context.Background(),
				ReadFileInput{Path: tc.path},
			)
			require.NoError(t, err)
			assert.Equal(t, "x", output.Content)
		})
	}
}

func TestReadFile_HomeRelativePath(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	require.NoError(t, os.WriteFile(
		filepath.Join(home, "f.txt"), []byte("hi"), 0o600,
	))

	exec := newReadFileExecutor(t, workspace)

	output, err := exec.ReadFile(context.Background(), ReadFileInput{
		Path: "~/f.txt",
	})
	require.NoError(t, err)
	assert.Equal(t, "hi", output.Content)
}

func TestReadFile_SymlinkedPath(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	realFile := filepath.Join(workspace, "real.txt")
	require.NoError(t, os.WriteFile(realFile, []byte("hi"), 0o600))

	link := filepath.Join(workspace, "link.txt")
	require.NoError(t, os.Symlink(realFile, link))

	exec := newReadFileExecutor(t, workspace)

	output, err := exec.ReadFile(
		context.Background(),
		ReadFileInput{Path: link},
	)
	require.NoError(t, err)
	assert.Equal(t, "hi", output.Content)
}

func TestReadFile_MissingPath(t *testing.T) {
	t.Parallel()

	exec := newReadFileExecutor(t, t.TempDir())

	_, err := exec.ReadFile(context.Background(), ReadFileInput{
		Path: "does-not-exist.txt",
	})
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

func TestReadFile_NotARegularFile(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	sub := filepath.Join(workspace, "sub")
	require.NoError(t, os.Mkdir(sub, 0o750))

	exec := newReadFileExecutor(t, workspace)

	_, err := exec.ReadFile(context.Background(), ReadFileInput{
		Path: "sub",
	})
	require.ErrorIs(t, err, ErrNotRegularFile)
}

func TestReadFile_PermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not enforced the same way on windows")
	}

	t.Parallel()

	workspace := t.TempDir()
	locked := filepath.Join(workspace, "locked.txt")
	require.NoError(t, os.WriteFile(locked, []byte("secret"), 0o600))
	require.NoError(t, os.Chmod(locked, 0o000))

	t.Cleanup(func() {
		_ = os.Chmod(locked, 0o600)
	})

	exec := newReadFileExecutor(t, workspace)

	_, err := exec.ReadFile(context.Background(), ReadFileInput{
		Path: "locked.txt",
	})
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
}

func TestReadFile_BinaryContentRejected(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "bin.dat"),
		[]byte("hello\x00world"),
		0o600,
	))

	exec := newReadFileExecutor(t, workspace)

	_, err := exec.ReadFile(context.Background(), ReadFileInput{
		Path: "bin.dat",
	})
	require.ErrorIs(t, err, ErrBinaryContent)
}

func TestReadFile_EmptyFile(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "empty.txt"), []byte(""), 0o600,
	))

	exec := newReadFileExecutor(t, workspace)

	output, err := exec.ReadFile(context.Background(), ReadFileInput{
		Path: "empty.txt",
	})
	require.NoError(t, err)
	assert.Equal(t, "", output.Content)
	assert.Equal(t, 1, output.FirstLine)
	assert.Equal(t, 0, output.LastLine)
	assert.Equal(t, 0, output.TotalLines)
	assert.Equal(t, 0, output.NextOffset)
	assert.False(t, output.Truncated)
	assert.Equal(t, hashBytes([]byte("")), output.SHA256)
}

func TestReadFile_NoTrailingNewline(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	content := "line1\nline2"
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "a.txt"), []byte(content), 0o600,
	))

	exec := newReadFileExecutor(t, workspace)

	output, err := exec.ReadFile(context.Background(), ReadFileInput{
		Path: "a.txt",
	})
	require.NoError(t, err)
	assert.Equal(t, content, output.Content)
	assert.Equal(t, 2, output.TotalLines)
	assert.False(t, output.Truncated)
}

func TestReadFile_CRLFLineEndings(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	content := "line1\r\nline2\r\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "a.txt"), []byte(content), 0o600,
	))

	exec := newReadFileExecutor(t, workspace)

	output, err := exec.ReadFile(context.Background(), ReadFileInput{
		Path: "a.txt",
	})
	require.NoError(t, err)
	assert.Equal(t, content, output.Content)
	assert.Equal(t, 2, output.TotalLines)
}

func TestReadFile_MultiByteUnicode(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	content := "café\n日本語\n\U0001F600\n" //nolint:gosmopolitan // non-ASCII text is the point of this test
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "a.txt"), []byte(content), 0o600,
	))

	exec := newReadFileExecutor(t, workspace)

	output, err := exec.ReadFile(context.Background(), ReadFileInput{
		Path: "a.txt",
	})
	require.NoError(t, err)
	assert.Equal(t, content, output.Content)
	assert.Equal(t, 3, output.TotalLines)
}

func TestReadFile_OffsetPastEnd(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "a.txt"), []byte("line1\nline2\n"), 0o600,
	))

	exec := newReadFileExecutor(t, workspace)

	output, err := exec.ReadFile(context.Background(), ReadFileInput{
		Path:   "a.txt",
		Offset: 10,
	})
	require.NoError(t, err)
	assert.Equal(t, "", output.Content)
	assert.Equal(t, 10, output.FirstLine)
	assert.Equal(t, 9, output.LastLine)
	assert.Equal(t, 2, output.TotalLines)
	assert.Equal(t, 0, output.NextOffset)
	assert.False(t, output.Truncated)
}

func TestReadFile_OffsetZeroOrNegativeDefaultsToFirstLine(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "a.txt"), []byte("line1\nline2\n"), 0o600,
	))

	exec := newReadFileExecutor(t, workspace)

	testCases := []struct {
		name   string
		offset int
	}{
		{name: "zero", offset: 0},
		{name: "negative", offset: -5},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			output, err := exec.ReadFile(context.Background(), ReadFileInput{
				Path:   "a.txt",
				Offset: tc.offset,
			})
			require.NoError(t, err)
			assert.Equal(t, 1, output.FirstLine)
		})
	}
}

func TestReadFile_LimitTruncatesWithNextOffset(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "a.txt"),
		[]byte("l1\nl2\nl3\nl4\nl5\n"),
		0o600,
	))

	exec := newReadFileExecutor(t, workspace)

	output, err := exec.ReadFile(context.Background(), ReadFileInput{
		Path:  "a.txt",
		Limit: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, "l1\nl2", output.Content)
	assert.Equal(t, 1, output.FirstLine)
	assert.Equal(t, 2, output.LastLine)
	assert.Equal(t, 5, output.TotalLines)
	assert.True(t, output.Truncated)
	assert.Equal(t, 3, output.NextOffset)

	next, err := exec.ReadFile(context.Background(), ReadFileInput{
		Path:   "a.txt",
		Offset: output.NextOffset,
		Limit:  2,
	})
	require.NoError(t, err)
	assert.Equal(t, "l3\nl4", next.Content)
	assert.True(t, next.Truncated)
	assert.Equal(t, 5, next.NextOffset)

	last, err := exec.ReadFile(context.Background(), ReadFileInput{
		Path:   "a.txt",
		Offset: next.NextOffset,
		Limit:  2,
	})
	require.NoError(t, err)
	assert.Equal(t, "l5\n", last.Content)
	assert.False(t, last.Truncated)
	assert.Equal(t, 0, last.NextOffset)
}

func TestReadFile_LimitClampedToMax(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "a.txt"), []byte("l1\nl2\nl3\n"), 0o600,
	))

	exec, err := NewExecutor(Options{
		Workspace: workspace,
		Limits:    Limits{MaxReadLines: 2},
	})
	require.NoError(t, err)

	output, err := exec.ReadFile(context.Background(), ReadFileInput{
		Path:  "a.txt",
		Limit: 1000,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, output.LastLine)
	assert.True(t, output.Truncated)
}

func TestReadFile_ZeroLimitUsesDefault(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "a.txt"), []byte("l1\nl2\nl3\n"), 0o600,
	))

	exec, err := NewExecutor(Options{
		Workspace: workspace,
		Limits:    Limits{MaxReadLines: 2},
	})
	require.NoError(t, err)

	output, err := exec.ReadFile(context.Background(), ReadFileInput{
		Path: "a.txt",
	})
	require.NoError(t, err)
	assert.Equal(t, 2, output.LastLine)
	assert.True(t, output.Truncated)
}

func TestReadFile_ExceedsMaxReadBytes(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "a.txt"), []byte("0123456789"), 0o600,
	))

	exec, err := NewExecutor(Options{
		Workspace: workspace,
		Limits:    Limits{MaxReadBytes: 4},
	})
	require.NoError(t, err)

	_, err = exec.ReadFile(context.Background(), ReadFileInput{
		Path: "a.txt",
	})
	require.ErrorIs(t, err, ErrLimitExceeded)
}

func TestReadFile_CancelledContext(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "a.txt"), []byte("x"), 0o600,
	))

	exec := newReadFileExecutor(t, workspace)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := exec.ReadFile(ctx, ReadFileInput{Path: "a.txt"})
	require.ErrorIs(t, err, context.Canceled)
}
