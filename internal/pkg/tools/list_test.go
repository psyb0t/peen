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

func newListFilesExecutor(t *testing.T, workspace string) *Executor {
	t.Helper()

	exec, err := NewExecutor(Options{Workspace: workspace})
	require.NoError(t, err)

	return exec
}

func TestListFiles_Success(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "b.txt"), []byte("b"), 0o600,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "a.txt"), []byte("a"), 0o600,
	))
	require.NoError(t, os.Mkdir(filepath.Join(workspace, "sub"), 0o750))

	exec := newListFilesExecutor(t, workspace)

	output, err := exec.ListFiles(context.Background(), ListFilesInput{})
	require.NoError(t, err)

	assert.Equal(t, workspace, output.Directory)
	assert.False(t, output.Truncated)
	require.Len(t, output.Entries, 3)
	assert.Equal(t, "a.txt", output.Entries[0].Path)
	assert.Equal(t, EntryTypeFile, output.Entries[0].Type)
	assert.Equal(t, "b.txt", output.Entries[1].Path)
	assert.Equal(t, "sub", output.Entries[2].Path)
	assert.Equal(t, EntryTypeDirectory, output.Entries[2].Type)
	assert.True(t, exec.Observed(workspace))
}

func TestListFiles_RelativeAbsoluteAndParentPaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	other := filepath.Join(root, "other")
	require.NoError(t, os.Mkdir(workspace, 0o750))
	require.NoError(t, os.Mkdir(other, 0o750))
	require.NoError(t, os.WriteFile(
		filepath.Join(other, "f.txt"), []byte("x"), 0o600,
	))

	exec := newListFilesExecutor(t, workspace)

	testCases := []struct {
		name string
		path string
		want string
	}{
		{name: "empty path is workspace", path: "", want: workspace},
		{name: "relative", path: ".", want: workspace},
		{name: "absolute", path: other, want: other},
		{name: "parent traversing", path: "../other", want: other},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			output, err := exec.ListFiles(
				context.Background(),
				ListFilesInput{Path: tc.path},
			)
			require.NoError(t, err)
			assert.Equal(t, tc.want, output.Directory)
		})
	}
}

func TestListFiles_HomeRelativePath(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	require.NoError(t, os.Mkdir(filepath.Join(home, "sub"), 0o750))
	require.NoError(t, os.WriteFile(
		filepath.Join(home, "sub", "f.txt"), []byte("x"), 0o600,
	))

	exec := newListFilesExecutor(t, workspace)

	output, err := exec.ListFiles(
		context.Background(),
		ListFilesInput{Path: "~/sub"},
	)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, "sub"), output.Directory)
	require.Len(t, output.Entries, 1)
	assert.Equal(t, "f.txt", output.Entries[0].Path)
}

func TestListFiles_SymlinkedDirectoryTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	require.NoError(t, os.Mkdir(realDir, 0o750))
	require.NoError(t, os.WriteFile(
		filepath.Join(realDir, "f.txt"), []byte("x"), 0o600,
	))

	link := filepath.Join(root, "link")
	require.NoError(t, os.Symlink(realDir, link))

	exec := newListFilesExecutor(t, root)

	output, err := exec.ListFiles(
		context.Background(),
		ListFilesInput{Path: link},
	)
	require.NoError(t, err)
	require.Len(t, output.Entries, 1)
	assert.Equal(t, "f.txt", output.Entries[0].Path)
}

func TestListFiles_SymlinkEntryNotDescended(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	realDir := filepath.Join(workspace, "real")
	require.NoError(t, os.Mkdir(realDir, 0o750))
	require.NoError(t, os.WriteFile(
		filepath.Join(realDir, "inner.txt"), []byte("x"), 0o600,
	))
	require.NoError(t, os.Symlink(realDir, filepath.Join(workspace, "link")))

	exec := newListFilesExecutor(t, workspace)

	output, err := exec.ListFiles(context.Background(), ListFilesInput{
		Recursive: true,
	})
	require.NoError(t, err)

	var linkEntry *FileEntry

	for i := range output.Entries {
		if output.Entries[i].Path == "link" {
			linkEntry = &output.Entries[i]
		}

		assert.NotEqual(t, "link/inner.txt", output.Entries[i].Path)
	}

	require.NotNil(t, linkEntry)
	assert.Equal(t, EntryTypeSymlink, linkEntry.Type)
}

func TestListFiles_MissingPath(t *testing.T) {
	t.Parallel()

	exec := newListFilesExecutor(t, t.TempDir())

	_, err := exec.ListFiles(
		context.Background(),
		ListFilesInput{Path: "does-not-exist"},
	)
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

func TestListFiles_NotADirectory(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	filePath := filepath.Join(workspace, "f.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("x"), 0o600))

	exec := newListFilesExecutor(t, workspace)

	_, err := exec.ListFiles(
		context.Background(),
		ListFilesInput{Path: filePath},
	)
	require.ErrorIs(t, err, ErrNotDirectory)
}

func TestListFiles_PermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not enforced the same way on windows")
	}

	t.Parallel()

	workspace := t.TempDir()
	locked := filepath.Join(workspace, "locked")
	require.NoError(t, os.Mkdir(locked, 0o750))
	require.NoError(t, os.WriteFile(
		filepath.Join(locked, "f.txt"), []byte("x"), 0o600,
	))
	require.NoError(t, os.Chmod(locked, 0o000))

	t.Cleanup(func() {
		_ = os.Chmod(locked, 0o700) //nolint:gosec // needs the exec bit restored so t.TempDir() cleanup can traverse and remove the locked directory
	})

	exec := newListFilesExecutor(t, workspace)

	_, err := exec.ListFiles(
		context.Background(),
		ListFilesInput{Path: locked},
	)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
}

func TestListFiles_UnreadableSubdirectoryIsSkipped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not enforced the same way on windows")
	}

	t.Parallel()

	workspace := t.TempDir()
	locked := filepath.Join(workspace, "locked")
	require.NoError(t, os.Mkdir(locked, 0o750))
	require.NoError(t, os.WriteFile(
		filepath.Join(locked, "f.txt"), []byte("x"), 0o600,
	))
	require.NoError(t, os.Chmod(locked, 0o000))

	t.Cleanup(func() {
		_ = os.Chmod(locked, 0o700) //nolint:gosec // needs the exec bit restored so t.TempDir() cleanup can traverse and remove the locked directory
	})

	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "visible.txt"), []byte("x"), 0o600,
	))

	exec := newListFilesExecutor(t, workspace)

	output, err := exec.ListFiles(context.Background(), ListFilesInput{
		Recursive: true,
	})
	require.NoError(t, err)

	paths := make([]string, 0, len(output.Entries))
	for _, entry := range output.Entries {
		paths = append(paths, entry.Path)
	}

	assert.Contains(t, paths, "locked")
	assert.Contains(t, paths, "visible.txt")
	assert.NotContains(t, paths, "locked/f.txt")
}

func TestListFiles_RecursiveMaxDepth(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	level1 := filepath.Join(workspace, "l1")
	level2 := filepath.Join(level1, "l2")
	require.NoError(t, os.MkdirAll(level2, 0o750))
	require.NoError(t, os.WriteFile(
		filepath.Join(level2, "deep.txt"), []byte("x"), 0o600,
	))

	exec := newListFilesExecutor(t, workspace)

	output, err := exec.ListFiles(context.Background(), ListFilesInput{
		Recursive: true,
		MaxDepth:  1,
	})
	require.NoError(t, err)
	require.Len(t, output.Entries, 1)
	assert.Equal(t, "l1", output.Entries[0].Path)

	output, err = exec.ListFiles(context.Background(), ListFilesInput{
		Recursive: true,
		MaxDepth:  2,
	})
	require.NoError(t, err)
	require.Len(t, output.Entries, 2)
	assert.Equal(t, "l1", output.Entries[0].Path)
	assert.Equal(t, "l1/l2", output.Entries[1].Path)
}

func TestListFiles_MaxDepthClampedToLimit(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	level1 := filepath.Join(workspace, "l1")
	level2 := filepath.Join(level1, "l2")
	require.NoError(t, os.MkdirAll(level2, 0o750))

	exec, err := NewExecutor(Options{
		Workspace: workspace,
		Limits:    Limits{MaxListDepth: 1},
	})
	require.NoError(t, err)

	output, err := exec.ListFiles(context.Background(), ListFilesInput{
		Recursive: true,
		MaxDepth:  50,
	})
	require.NoError(t, err)
	require.Len(t, output.Entries, 1)
	assert.Equal(t, "l1", output.Entries[0].Path)
}

func TestListFiles_MaxEntriesTruncates(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()

	for i := range 5 {
		name := filepath.Join(workspace, "f"+string(rune('a'+i))+".txt")
		require.NoError(t, os.WriteFile(name, []byte("x"), 0o600))
	}

	exec, err := NewExecutor(Options{
		Workspace: workspace,
		Limits:    Limits{MaxListEntries: 3},
	})
	require.NoError(t, err)

	output, err := exec.ListFiles(context.Background(), ListFilesInput{})
	require.NoError(t, err)
	assert.True(t, output.Truncated)
	assert.Len(t, output.Entries, 3)
}

func TestListFiles_StableOrdering(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	names := []string{"zeta.txt", "alpha.txt", "mu.txt"}

	for _, name := range names {
		require.NoError(t, os.WriteFile(
			filepath.Join(workspace, name), []byte("x"), 0o600,
		))
	}

	exec := newListFilesExecutor(t, workspace)

	output, err := exec.ListFiles(context.Background(), ListFilesInput{})
	require.NoError(t, err)
	require.Len(t, output.Entries, 3)
	assert.Equal(t, "alpha.txt", output.Entries[0].Path)
	assert.Equal(t, "mu.txt", output.Entries[1].Path)
	assert.Equal(t, "zeta.txt", output.Entries[2].Path)
}

func TestListFiles_CancelledContext(t *testing.T) {
	t.Parallel()

	exec := newListFilesExecutor(t, t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := exec.ListFiles(ctx, ListFilesInput{})
	require.ErrorIs(t, err, context.Canceled)
}
