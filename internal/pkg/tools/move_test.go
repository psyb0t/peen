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

func newMovePathTestExecutor(t *testing.T) *Executor {
	t.Helper()

	exec, err := NewExecutor(Options{
		Workspace: t.TempDir(),
	})
	require.NoError(t, err)

	return exec
}

// observeSource creates a source file with content, observes it on the
// executor, and returns its resolved path and content hash.
func observeSource(t *testing.T, exec *Executor, path string) string {
	t.Helper()

	content := []byte("move me")
	require.NoError(t, os.WriteFile(path, content, newFileMode))
	exec.observeContent(path, hashBytes(content))

	return path
}

func TestMovePath_Success(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		setup func(t *testing.T, exec *Executor) (source, destination string)
	}{
		{
			name: "relative paths",
			setup: func(t *testing.T, exec *Executor) (string, string) {
				t.Helper()

				source := observeSource(
					t, exec, filepath.Join(exec.Workspace(), "src.txt"),
				)

				return source, filepath.Join(exec.Workspace(), "dst.txt")
			},
		},
		{
			name: "absolute paths",
			setup: func(t *testing.T, exec *Executor) (string, string) {
				t.Helper()

				source := observeSource(
					t, exec, filepath.Join(exec.Workspace(), "abs-src.txt"),
				)

				return source, filepath.Join(exec.Workspace(), "abs-dst.txt")
			},
		},
		{
			name: "parent traversing destination outside workspace",
			setup: func(t *testing.T, exec *Executor) (string, string) {
				t.Helper()

				base := filepath.Dir(exec.Workspace())
				require.NoError(t, os.MkdirAll(base, newDirectoryMode))

				source := observeSource(
					t, exec, filepath.Join(exec.Workspace(), "escaping.txt"),
				)
				destination := filepath.Join(base, "escaped-dst.txt")

				return source, destination
			},
		},
		{
			name: "destination through symlinked directory",
			setup: func(t *testing.T, exec *Executor) (string, string) {
				t.Helper()

				realDir := filepath.Join(exec.Workspace(), "real")
				require.NoError(t, os.MkdirAll(realDir, newDirectoryMode))

				link := filepath.Join(exec.Workspace(), "link")
				require.NoError(t, os.Symlink(realDir, link))

				source := observeSource(
					t, exec, filepath.Join(exec.Workspace(), "linked-src.txt"),
				)
				destination := filepath.Join(link, "linked-dst.txt")

				return source, destination
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			exec := newMovePathTestExecutor(t)
			source, destination := tc.setup(t, exec)

			out, err := exec.MovePath(context.Background(), MovePathInput{
				Source:      source,
				Destination: destination,
			})
			require.NoError(t, err)

			assert.Equal(t, source, out.Source)
			assert.Equal(t, destination, out.Destination)

			_, statErr := os.Lstat(source)
			assert.True(t, os.IsNotExist(statErr))

			content, err := os.ReadFile(destination)
			require.NoError(t, err)
			assert.Equal(t, "move me", string(content))
			assert.True(t, exec.Observed(destination))
		})
	}
}

func TestMovePath_MovesSymlinkItself(t *testing.T) {
	t.Parallel()

	exec := newMovePathTestExecutor(t)

	target := filepath.Join(exec.Workspace(), "target.txt")
	require.NoError(t, os.WriteFile(target, []byte("data"), newFileMode))

	link := filepath.Join(exec.Workspace(), "link.txt")
	require.NoError(t, os.Symlink(target, link))
	exec.observe(link)

	destination := filepath.Join(exec.Workspace(), "moved-link.txt")

	out, err := exec.MovePath(context.Background(), MovePathInput{
		Source:      link,
		Destination: destination,
	})
	require.NoError(t, err)

	info, err := os.Lstat(out.Destination)
	require.NoError(t, err)
	assert.True(t, info.Mode()&os.ModeSymlink != 0)

	resolved, err := os.Readlink(out.Destination)
	require.NoError(t, err)
	assert.Equal(t, target, resolved)
}

func TestMovePath_MissingObservation(t *testing.T) {
	t.Parallel()

	exec := newMovePathTestExecutor(t)
	source := filepath.Join(exec.Workspace(), "unobserved.txt")
	require.NoError(t, os.WriteFile(source, []byte("x"), newFileMode))

	_, err := exec.MovePath(context.Background(), MovePathInput{
		Source:      source,
		Destination: filepath.Join(exec.Workspace(), "dst.txt"),
	})
	require.ErrorIs(t, err, ErrNotObserved)
}

func TestMovePath_MissingSource(t *testing.T) {
	t.Parallel()

	exec := newMovePathTestExecutor(t)

	_, err := exec.MovePath(context.Background(), MovePathInput{
		Source:      filepath.Join(exec.Workspace(), "missing.txt"),
		Destination: filepath.Join(exec.Workspace(), "dst.txt"),
	})
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

func TestMovePath_DestinationCollision(t *testing.T) {
	t.Parallel()

	exec := newMovePathTestExecutor(t)
	source := observeSource(
		t, exec, filepath.Join(exec.Workspace(), "collide-src.txt"),
	)

	destination := filepath.Join(exec.Workspace(), "collide-dst.txt")
	require.NoError(t, os.WriteFile(destination, []byte("taken"), newFileMode))

	_, err := exec.MovePath(context.Background(), MovePathInput{
		Source:      source,
		Destination: destination,
	})
	require.ErrorIs(t, err, commerr.ErrAlreadyExists)

	content, err := os.ReadFile(destination)
	require.NoError(t, err)
	assert.Equal(t, "taken", string(content))
}

func TestMovePath_ValidationAndCancellation(t *testing.T) {
	t.Parallel()

	exec := newMovePathTestExecutor(t)

	testCases := []struct {
		name    string
		ctx     func() context.Context
		source  string
		dest    string
		wantErr error
	}{
		{
			name:    "empty source",
			ctx:     context.Background,
			source:  "",
			dest:    "dst.txt",
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name:    "empty destination",
			ctx:     context.Background,
			source:  "src.txt",
			dest:    "",
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "already cancelled context",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()

				return ctx
			},
			source:  "src.txt",
			dest:    "dst.txt",
			wantErr: context.Canceled,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := exec.MovePath(tc.ctx(), MovePathInput{
				Source:      tc.source,
				Destination: tc.dest,
			})
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestMovePath_ConcurrentMovesSerialize(t *testing.T) {
	t.Parallel()

	exec := newMovePathTestExecutor(t)

	sourceA := observeSource(
		t, exec, filepath.Join(exec.Workspace(), "conc-src-a.txt"),
	)
	sourceB := observeSource(
		t, exec, filepath.Join(exec.Workspace(), "conc-src-b.txt"),
	)
	destA := filepath.Join(exec.Workspace(), "conc-dst-a.txt")
	destB := filepath.Join(exec.Workspace(), "conc-dst-b.txt")

	var wg sync.WaitGroup

	errs := make([]error, 2)
	sources := []string{sourceA, sourceB}
	destinations := []string{destA, destB}

	for i := range sources {
		wg.Add(1)

		go func(index int) {
			defer wg.Done()

			_, err := exec.MovePath(context.Background(), MovePathInput{
				Source:      sources[index],
				Destination: destinations[index],
			})
			errs[index] = err
		}(i)
	}

	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}

	for _, dest := range destinations {
		_, err := os.Stat(dest)
		require.NoError(t, err)
	}
}
