package tools

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/workspacefs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testDirectoryMode = 0o700

type malformedWorkingDirectoryFilesystem struct {
	workspacefs.FS
	canonical string
	info      fs.FileInfo
}

func (f malformedWorkingDirectoryFilesystem) RealPath(string) (string, error) {
	return f.canonical, nil
}

func (f malformedWorkingDirectoryFilesystem) Stat(string) (fs.FileInfo, error) {
	return f.info, nil
}

func TestVirtualPathsResolveFromCurrentDirectory(t *testing.T) {
	t.Parallel()

	filesystem := workspacefs.ReadOnly{Source: fstest.MapFS{
		"nested/file.txt": {Data: []byte("nested")},
	}}
	executor, err := NewExecutor(Options{
		Workspace: "nested", Filesystem: filesystem, Virtual: true,
	})
	require.NoError(t, err)

	testCases := []struct {
		name string
		path string
		want string
		err  error
	}{
		{name: "file", path: "file.txt", want: "nested/file.txt"},
		{name: "current", path: ".", want: "nested"},
		{name: "empty", want: "nested"},
		{name: "parent", path: "..", want: "."},
		{name: "escape", path: "../..", err: fs.ErrInvalid},
		{name: "absolute", path: "/file.txt", err: ErrInvalidOptions},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, pathErr := executor.ResolvePath(tc.path)
			require.ErrorIs(t, pathErr, tc.err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestAtCanonicalDirectoryRestoresFromTheVirtualRoot(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		from    string
		to      string
		invalid bool
	}{
		{name: "enter", from: ".", to: "nested"},
		{name: "repeat", from: "nested", to: "nested"},
		{name: "parent", from: "nested", to: "."},
		{name: "sibling", from: "nested", to: "other"},
		{name: "absolute virtual checkpoint", from: "nested", to: "/other", invalid: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			filesystem := workspacefs.ReadOnly{Source: fstest.MapFS{
				"nested/file": {Data: []byte("nested")},
				"other/file":  {Data: []byte("other")},
			}}
			base, err := NewExecutor(Options{
				Workspace: tc.from, Filesystem: filesystem, Virtual: true,
			})
			require.NoError(t, err)
			executor := &JobExecutor{Executor: base}
			restored, err := executor.AtCanonicalDirectory(t.Context(), tc.to)
			if tc.invalid {
				require.Error(t, err)
				assert.Nil(t, restored)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.to, restored.Workspace())
			assert.Equal(t, tc.from, executor.Workspace())
		})
	}
}

func TestAtDirectoryCanonicalizesAliasesAndRejectsCancellation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := filepath.Join(root, "quoted \"\u4e16\u754c\"")
	require.NoError(t, os.Mkdir(directory, testDirectoryMode))
	require.NoError(t, os.Symlink(directory, filepath.Join(root, "alias")))
	executor, err := NewExecutor(Options{Workspace: root})
	require.NoError(t, err)
	fork, err := executor.AtDirectory(context.Background(), "alias")
	require.NoError(t, err)
	assert.Equal(t, directory, fork.Workspace())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = executor.AtDirectory(ctx, "alias")
	require.ErrorIs(t, err, context.Canceled)
}

func TestAtDirectoryRetainsSessionRemovalGuard(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "child"), testDirectoryMode))
	executor, err := NewExecutor(Options{Workspace: root})
	require.NoError(t, err)
	fork, err := executor.AtDirectory(context.Background(), "child")
	require.NoError(t, err)
	_, err = fork.RemovePath(context.Background(), RemovePathInput{Path: "..", Recursive: true})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

func TestDirectoryForkRequiresItsOwnReadBeforeChangingFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	file := filepath.Join(root, "file.txt")
	require.NoError(t, os.WriteFile(file, []byte("initial"), testFileMode))
	executor, err := NewExecutor(Options{Workspace: root})
	require.NoError(t, err)
	read, err := executor.ReadFile(context.Background(), ReadFileInput{Path: "file.txt"})
	require.NoError(t, err)
	registry := newTestRegistry(t, nil)
	parent, err := NewJobExecutor(executor, registry, uuid.New())
	require.NoError(t, err)
	child, err := parent.Fork(context.Background())
	require.NoError(t, err)
	_, err = child.WriteFile(context.Background(), WriteFileInput{Path: "file.txt", Content: "child", ExpectedSHA256: read.SHA256})
	require.ErrorIs(t, err, ErrNotObserved)
	_, err = child.ReadFile(context.Background(), ReadFileInput{Path: "file.txt"})
	require.NoError(t, err)
	_, err = parent.WriteFile(context.Background(), WriteFileInput{Path: "file.txt", Content: "parent", ExpectedSHA256: read.SHA256})
	require.NoError(t, err)
	_, err = child.WriteFile(context.Background(), WriteFileInput{Path: "file.txt", Content: "child", ExpectedSHA256: read.SHA256})
	require.ErrorIs(t, err, ErrStaleHash)
}

func TestAtDirectoryRejectsVirtualEscapes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "nested"), testDirectoryMode))
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(root, "outside")))
	handle, err := os.OpenRoot(root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	executor, err := NewExecutor(Options{Workspace: ".", Virtual: true, Filesystem: workspacefs.Root{Handle: handle}})
	require.NoError(t, err)
	for _, target := range []string{"..", "/tmp", "outside"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			_, err := executor.AtDirectory(context.Background(), target)
			require.Error(t, err)
			assert.Equal(t, ".", executor.Workspace())
		})
	}
}

func TestAtDirectoryRejectsMalformedVirtualCanonicalPaths(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	info, err := os.Stat(directory)
	require.NoError(t, err)
	testCases := []struct {
		name      string
		canonical string
	}{
		{name: "absolute", canonical: "/absolute"},
		{name: "parent", canonical: ".."},
		{name: "escape", canonical: "../outside"},
		{name: "unclean", canonical: "a/../b"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			filesystem := malformedWorkingDirectoryFilesystem{
				canonical: tc.canonical,
				info:      info,
			}
			executor, err := NewExecutor(Options{
				Workspace: ".", Filesystem: filesystem, Virtual: true,
			})
			require.NoError(t, err)
			fork, err := executor.AtDirectory(context.Background(), ".")
			require.ErrorIs(t, err, fs.ErrInvalid)
			assert.Nil(t, fork)
			assert.Equal(t, ".", executor.Workspace())
		})
	}
}

func TestAtDirectoryValidatesAndPreservesTheOriginal(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name string
		path string
		want string
		err  error
	}{
		{name: "child", path: "child", want: "child"},
		{name: "same", path: ".", want: "."},
		{name: "empty", err: commerr.ErrValidationFailed},
		{name: "missing", path: "missing", err: fs.ErrNotExist},
		{name: "file", path: "file.txt", err: ErrNotDirectory},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(root, "child"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(root, "file.txt"), []byte("seen"), testFileMode))
			executor, err := NewExecutor(Options{Workspace: root})
			require.NoError(t, err)
			executor.observe(filepath.Join(root, "file.txt"))
			fork, err := executor.AtDirectory(context.Background(), tc.path)
			require.ErrorIs(t, err, tc.err)
			assert.Equal(t, root, executor.Workspace())
			if tc.err != nil {
				require.Nil(t, fork)

				return
			}
			require.NotNil(t, fork)
			assert.Equal(t, filepath.Join(root, tc.want), fork.Workspace())
			assert.True(t, fork.Observed(filepath.Join(root, "file.txt")))
			fork.observe(filepath.Join(root, "another.txt"))
			assert.False(t, executor.Observed(filepath.Join(root, "another.txt")))
		})
	}
}
