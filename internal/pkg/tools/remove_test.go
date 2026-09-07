package tools

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const removeTestTreeMaxEntries = 2

func newRemovePathTestExecutor(t *testing.T, limits Limits) *Executor {
	t.Helper()

	exec, err := NewExecutor(Options{
		Workspace: t.TempDir(),
		Limits:    limits,
	})
	require.NoError(t, err)

	return exec
}

// observeRemovable writes content at path, observes it, and returns the
// path and content hash so a test can remove it.
func observeRemovable(t *testing.T, exec *Executor, path string) string {
	t.Helper()

	content := []byte("remove me")
	require.NoError(t, os.WriteFile(path, content, newFileMode))
	exec.observeContent(path, hashBytes(content))

	return path
}

func TestRemovePath_RemovesRegularFile(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		setup func(t *testing.T, exec *Executor) string
	}{
		{
			name: "relative path",
			setup: func(t *testing.T, exec *Executor) string {
				t.Helper()

				return observeRemovable(
					t, exec, filepath.Join(exec.Workspace(), "rel.txt"),
				)
			},
		},
		{
			name: "absolute path",
			setup: func(t *testing.T, exec *Executor) string {
				t.Helper()

				return observeRemovable(
					t, exec, filepath.Join(exec.Workspace(), "abs.txt"),
				)
			},
		},
		{
			name: "parent traversing path outside workspace",
			setup: func(t *testing.T, exec *Executor) string {
				t.Helper()

				base := filepath.Dir(exec.Workspace())
				require.NoError(t, os.MkdirAll(base, newDirectoryMode))

				return observeRemovable(
					t, exec, filepath.Join(base, "escaped-remove.txt"),
				)
			},
		},
		{
			name: "through symlinked directory",
			setup: func(t *testing.T, exec *Executor) string {
				t.Helper()

				realDir := filepath.Join(exec.Workspace(), "real")
				require.NoError(t, os.MkdirAll(realDir, newDirectoryMode))

				link := filepath.Join(exec.Workspace(), "link")
				require.NoError(t, os.Symlink(realDir, link))

				return observeRemovable(
					t, exec, filepath.Join(link, "linked.txt"),
				)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			exec := newRemovePathTestExecutor(t, Limits{})
			path := tc.setup(t, exec)
			hash := hashBytes([]byte("remove me"))

			out, err := exec.RemovePath(context.Background(), RemovePathInput{
				Path:           path,
				ExpectedSHA256: hash,
			})
			require.NoError(t, err)
			assert.Equal(t, removedOne, out.Removed)

			_, statErr := os.Lstat(path)
			assert.True(t, os.IsNotExist(statErr))
		})
	}
}

func TestRemovePath_RemovesSymlinkWithoutFollowing(t *testing.T) {
	t.Parallel()

	exec := newRemovePathTestExecutor(t, Limits{})

	target := filepath.Join(exec.Workspace(), "target.txt")
	require.NoError(t, os.WriteFile(target, []byte("keep me"), newFileMode))

	link := filepath.Join(exec.Workspace(), "link.txt")
	require.NoError(t, os.Symlink(target, link))
	exec.observe(link)

	out, err := exec.RemovePath(
		context.Background(),
		RemovePathInput{Path: link},
	)
	require.NoError(t, err)
	assert.Equal(t, removedOne, out.Removed)

	_, statErr := os.Lstat(link)
	assert.True(t, os.IsNotExist(statErr))

	content, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "keep me", string(content))
}

func TestRemovePath_RegularFileHashChecks(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		observe bool
		hash    string
		wantErr error
	}{
		{
			name:    "missing observation",
			observe: false,
			hash:    hashBytes([]byte("content")),
			wantErr: ErrNotObserved,
		},
		{
			name:    "missing hash",
			observe: true,
			hash:    "",
			wantErr: ErrHashRequired,
		},
		{
			name:    "stale hash",
			observe: true,
			hash:    hashBytes([]byte("something else")),
			wantErr: ErrStaleHash,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			exec := newRemovePathTestExecutor(t, Limits{})
			path := filepath.Join(exec.Workspace(), "checked.txt")
			require.NoError(t, os.WriteFile(path, []byte("content"), newFileMode))

			if tc.observe {
				exec.observeContent(path, hashBytes([]byte("content")))
			}

			_, err := exec.RemovePath(context.Background(), RemovePathInput{
				Path:           path,
				ExpectedSHA256: tc.hash,
			})
			require.ErrorIs(t, err, tc.wantErr)

			_, statErr := os.Lstat(path)
			require.NoError(t, statErr)
		})
	}
}

func TestRemovePath_RejectsListedOnlyRegularFile(t *testing.T) {
	t.Parallel()

	exec := newRemovePathTestExecutor(t, Limits{})
	path := filepath.Join(exec.Workspace(), "listed-only.txt")
	content := []byte("keep")
	require.NoError(t, os.WriteFile(path, content, newFileMode))
	exec.observe(path)

	_, err := exec.RemovePath(context.Background(), RemovePathInput{
		Path:           path,
		ExpectedSHA256: hashBytes(content),
	})
	require.ErrorIs(t, err, ErrHashRequired)

	written, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, content, written)
}

func TestRemovePath_EmptyDirectory(t *testing.T) {
	t.Parallel()

	exec := newRemovePathTestExecutor(t, Limits{})
	path := filepath.Join(exec.Workspace(), "emptydir")
	require.NoError(t, os.MkdirAll(path, newDirectoryMode))
	exec.observe(path)

	out, err := exec.RemovePath(
		context.Background(),
		RemovePathInput{Path: path},
	)
	require.NoError(t, err)
	assert.Equal(t, removedOne, out.Removed)

	_, statErr := os.Lstat(path)
	assert.True(t, os.IsNotExist(statErr))
}

func TestRemovePath_NonEmptyDirectoryRequiresRecursive(t *testing.T) {
	t.Parallel()

	exec := newRemovePathTestExecutor(t, Limits{})
	path := filepath.Join(exec.Workspace(), "fulldir")
	require.NoError(t, os.MkdirAll(path, newDirectoryMode))
	require.NoError(
		t,
		os.WriteFile(filepath.Join(path, "child.txt"), []byte("x"), newFileMode),
	)
	exec.observe(path)

	_, err := exec.RemovePath(
		context.Background(),
		RemovePathInput{Path: path},
	)
	require.ErrorIs(t, err, ErrDirectoryNotEmpty)

	_, statErr := os.Lstat(path)
	require.NoError(t, statErr)
}

func TestRemovePath_RecursiveDirectoryTree(t *testing.T) {
	t.Parallel()

	exec := newRemovePathTestExecutor(t, Limits{})
	root := filepath.Join(exec.Workspace(), "tree")
	sub := filepath.Join(root, "sub")
	require.NoError(t, os.MkdirAll(sub, newDirectoryMode))
	require.NoError(
		t,
		os.WriteFile(filepath.Join(root, "one.txt"), []byte("1"), newFileMode),
	)
	require.NoError(
		t,
		os.WriteFile(filepath.Join(sub, "two.txt"), []byte("2"), newFileMode),
	)
	exec.observe(root)

	const wantRemoved = 4

	out, err := exec.RemovePath(context.Background(), RemovePathInput{
		Path:      root,
		Recursive: true,
	})
	require.NoError(t, err)
	assert.Equal(t, wantRemoved, out.Removed)

	_, statErr := os.Lstat(root)
	assert.True(t, os.IsNotExist(statErr))
}

func TestRemovePath_RecursiveBoundExceeded(t *testing.T) {
	t.Parallel()

	exec := newRemovePathTestExecutor(
		t,
		Limits{MaxRemoveEntries: removeTestTreeMaxEntries},
	)
	root := filepath.Join(exec.Workspace(), "bigtree")
	require.NoError(t, os.MkdirAll(root, newDirectoryMode))
	require.NoError(
		t,
		os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), newFileMode),
	)
	require.NoError(
		t,
		os.WriteFile(filepath.Join(root, "b.txt"), []byte("b"), newFileMode),
	)
	exec.observe(root)

	_, err := exec.RemovePath(context.Background(), RemovePathInput{
		Path:      root,
		Recursive: true,
	})
	require.ErrorIs(t, err, ErrLimitExceeded)

	entries, statErr := os.ReadDir(root)
	require.NoError(t, statErr)
	assert.Len(t, entries, 2)
}

func TestRemovePath_DirectoryRejectsHash(t *testing.T) {
	t.Parallel()

	exec := newRemovePathTestExecutor(t, Limits{})
	path := filepath.Join(exec.Workspace(), "hashed-dir")
	require.NoError(t, os.MkdirAll(path, newDirectoryMode))
	exec.observe(path)

	_, err := exec.RemovePath(context.Background(), RemovePathInput{
		Path:           path,
		ExpectedSHA256: hashBytes([]byte("irrelevant")),
	})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

func TestRemovePath_ValidationAndGuards(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		path    func(exec *Executor) string
		wantErr error
	}{
		{
			name:    "empty path",
			path:    func(_ *Executor) string { return "" },
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "missing path",
			path: func(exec *Executor) string {
				return filepath.Join(exec.Workspace(), "nope.txt")
			},
			wantErr: commerr.ErrNotFound,
		},
		{
			name:    "refuses workspace root",
			path:    func(exec *Executor) string { return exec.Workspace() },
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name:    "refuses filesystem root",
			path:    func(_ *Executor) string { return rootPath },
			wantErr: commerr.ErrValidationFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			exec := newRemovePathTestExecutor(t, Limits{})

			_, err := exec.RemovePath(context.Background(), RemovePathInput{
				Path: tc.path(exec),
			})
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestRemovePath_CancelledContext(t *testing.T) {
	t.Parallel()

	exec := newRemovePathTestExecutor(t, Limits{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := exec.RemovePath(ctx, RemovePathInput{Path: "whatever.txt"})
	require.ErrorIs(t, err, context.Canceled)
}

func TestRemovePath_ConcurrentRemovalsSerialize(t *testing.T) {
	t.Parallel()

	exec := newRemovePathTestExecutor(t, Limits{})
	pathA := observeRemovable(
		t, exec, filepath.Join(exec.Workspace(), "conc-a.txt"),
	)
	pathB := observeRemovable(
		t, exec, filepath.Join(exec.Workspace(), "conc-b.txt"),
	)
	hash := hashBytes([]byte("remove me"))

	var wg sync.WaitGroup

	errs := make([]error, 2)
	paths := []string{pathA, pathB}

	for i, path := range paths {
		wg.Add(1)

		go func(index int, target string) {
			defer wg.Done()

			_, err := exec.RemovePath(context.Background(), RemovePathInput{
				Path:           target,
				ExpectedSHA256: hash,
			})
			errs[index] = err
		}(i, path)
	}

	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}

	for _, path := range paths {
		_, statErr := os.Lstat(path)
		assert.True(t, os.IsNotExist(statErr))
	}
}
