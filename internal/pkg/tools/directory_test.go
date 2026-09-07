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

const directoryTestMaxEntries = 2

func newMakeDirectoryTestExecutor(t *testing.T, limits Limits) *Executor {
	t.Helper()

	exec, err := NewExecutor(Options{
		Workspace: t.TempDir(),
		Limits:    limits,
	})
	require.NoError(t, err)

	return exec
}

func TestMakeDirectory_CreatesMissingAncestors(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		setup func(t *testing.T, exec *Executor) (string, []string)
	}{
		{
			name: "relative path with nested ancestors",
			setup: func(_ *testing.T, exec *Executor) (string, []string) {
				want := []string{
					filepath.Join(exec.Workspace(), "a"),
					filepath.Join(exec.Workspace(), "a", "b"),
					filepath.Join(exec.Workspace(), "a", "b", "c"),
				}

				return filepath.Join("a", "b", "c"), want
			},
		},
		{
			name: "absolute path",
			setup: func(_ *testing.T, exec *Executor) (string, []string) {
				target := filepath.Join(exec.Workspace(), "abs", "dir")
				want := []string{
					filepath.Join(exec.Workspace(), "abs"),
					target,
				}

				return target, want
			},
		},
		{
			name: "parent traversing path outside workspace",
			setup: func(t *testing.T, exec *Executor) (string, []string) {
				t.Helper()

				base := filepath.Dir(exec.Workspace())
				target := filepath.Join(base, "escaped-dir", "leaf")
				want := []string{
					filepath.Join(base, "escaped-dir"),
					target,
				}

				return filepath.Join("..", "escaped-dir", "leaf"), want
			},
		},
		{
			name: "through symlinked directory",
			setup: func(t *testing.T, exec *Executor) (string, []string) {
				t.Helper()

				realDir := filepath.Join(exec.Workspace(), "real")
				require.NoError(t, os.MkdirAll(realDir, newDirectoryMode))

				link := filepath.Join(exec.Workspace(), "link")
				require.NoError(t, os.Symlink(realDir, link))

				target := filepath.Join(link, "newsub")
				want := []string{target}

				return filepath.Join("link", "newsub"), want
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			exec := newMakeDirectoryTestExecutor(t, Limits{})
			path, want := tc.setup(t, exec)

			out, err := exec.MakeDirectory(
				context.Background(),
				MakeDirectoryInput{Path: path},
			)
			require.NoError(t, err)

			assert.Equal(t, want, out.Created)

			info, statErr := os.Stat(out.Path)
			require.NoError(t, statErr)
			assert.True(t, info.IsDir())
			assert.True(t, exec.Observed(out.Path))
		})
	}
}

func TestMakeDirectory_TildeRelativePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	exec := newMakeDirectoryTestExecutor(t, Limits{})

	out, err := exec.MakeDirectory(
		context.Background(),
		MakeDirectoryInput{Path: "~/homedir"},
	)
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(home, "homedir"), out.Path)
	assert.Equal(t, []string{out.Path}, out.Created)
}

func TestMakeDirectory_AlreadyExisting(t *testing.T) {
	t.Parallel()

	exec := newMakeDirectoryTestExecutor(t, Limits{})
	path := filepath.Join(exec.Workspace(), "already")
	require.NoError(t, os.MkdirAll(path, newDirectoryMode))

	out, err := exec.MakeDirectory(
		context.Background(),
		MakeDirectoryInput{Path: path},
	)
	require.NoError(t, err)

	assert.Empty(t, out.Created)
	assert.True(t, exec.Observed(out.Path))
}

func TestMakeDirectory_RejectsNonDirectoryPath(t *testing.T) {
	t.Parallel()

	exec := newMakeDirectoryTestExecutor(t, Limits{})
	path := filepath.Join(exec.Workspace(), "afile")
	require.NoError(t, os.WriteFile(path, []byte("x"), newFileMode))

	_, err := exec.MakeDirectory(
		context.Background(),
		MakeDirectoryInput{Path: path},
	)
	require.ErrorIs(t, err, ErrNotDirectory)
}

func TestMakeDirectory_ValidationAndBounds(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		path    string
		limits  Limits
		wantErr error
	}{
		{
			name:    "empty path",
			path:    "",
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name:    "too many ancestors to create",
			path:    filepath.Join("too", "many", "levels"),
			limits:  Limits{MaxRemoveEntries: directoryTestMaxEntries},
			wantErr: ErrLimitExceeded,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			exec := newMakeDirectoryTestExecutor(t, tc.limits)

			_, err := exec.MakeDirectory(
				context.Background(),
				MakeDirectoryInput{Path: tc.path},
			)
			require.ErrorIs(t, err, tc.wantErr)

			if tc.path != "" {
				_, statErr := os.Stat(filepath.Join(exec.Workspace(), tc.path))
				assert.True(t, os.IsNotExist(statErr))
			}
		})
	}
}

func TestMakeDirectory_CancelledContext(t *testing.T) {
	t.Parallel()

	exec := newMakeDirectoryTestExecutor(t, Limits{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := exec.MakeDirectory(ctx, MakeDirectoryInput{Path: "whatever"})
	require.ErrorIs(t, err, context.Canceled)
}

func TestMakeDirectory_ConcurrentCallsSerialize(t *testing.T) {
	t.Parallel()

	exec := newMakeDirectoryTestExecutor(t, Limits{})

	var wg sync.WaitGroup

	errs := make([]error, 2)
	paths := []string{
		filepath.Join("shared", "one"),
		filepath.Join("shared", "two"),
	}

	for i, path := range paths {
		wg.Add(1)

		go func(index int, dirPath string) {
			defer wg.Done()

			_, err := exec.MakeDirectory(
				context.Background(),
				MakeDirectoryInput{Path: dirPath},
			)
			errs[index] = err
		}(i, path)
	}

	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}

	for _, path := range paths {
		info, err := os.Stat(filepath.Join(exec.Workspace(), path))
		require.NoError(t, err)
		assert.True(t, info.IsDir())
	}
}
