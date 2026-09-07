package tools

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	writeTestReadOnlyDirMode = fs.FileMode(0o555)
	writeTestExistingMode    = fs.FileMode(0o640)
	writeTestSmallBound      = 4
)

func newWriteFileTestExecutor(t *testing.T, limits Limits) *Executor {
	t.Helper()

	exec, err := NewExecutor(Options{
		Workspace: t.TempDir(),
		Limits:    limits,
	})
	require.NoError(t, err)

	return exec
}

func TestWriteFile_CreatesNewFile(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		setup   func(t *testing.T, exec *Executor) string
		content string
	}{
		{
			name: "relative path",
			setup: func(_ *testing.T, _ *Executor) string {
				return "created.txt"
			},
			content: "hello relative",
		},
		{
			name: "absolute path",
			setup: func(_ *testing.T, exec *Executor) string {
				return filepath.Join(exec.Workspace(), "abs-created.txt")
			},
			content: "hello absolute",
		},
		{
			name: "parent traversing path outside workspace",
			setup: func(t *testing.T, exec *Executor) string {
				t.Helper()

				base := filepath.Dir(exec.Workspace())
				sibling := filepath.Join(base, "sibling")
				require.NoError(t, os.MkdirAll(sibling, newDirectoryMode))

				return filepath.Join("..", "sibling", "escaped.txt")
			},
			content: "hello outside",
		},
		{
			name: "through symlinked directory",
			setup: func(t *testing.T, exec *Executor) string {
				t.Helper()

				realDir := filepath.Join(exec.Workspace(), "real")
				require.NoError(t, os.MkdirAll(realDir, newDirectoryMode))

				link := filepath.Join(exec.Workspace(), "link")
				require.NoError(t, os.Symlink(realDir, link))

				return filepath.Join("link", "through.txt")
			},
			content: "hello through link",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			exec := newWriteFileTestExecutor(t, Limits{})
			path := tc.setup(t, exec)

			out, err := exec.WriteFile(context.Background(), WriteFileInput{
				Path:    path,
				Content: tc.content,
			})
			require.NoError(t, err)

			assert.True(t, out.Created)
			assert.Equal(t, len(tc.content), out.Bytes)
			assert.Equal(t, hashBytes([]byte(tc.content)), out.SHA256)

			written, err := os.ReadFile(out.Path)
			require.NoError(t, err)
			assert.Equal(t, tc.content, string(written))
			assert.True(t, exec.Observed(out.Path))
		})
	}
}

func TestWriteFile_TildeRelativePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	exec := newWriteFileTestExecutor(t, Limits{})

	out, err := exec.WriteFile(context.Background(), WriteFileInput{
		Path:    "~/homefile.txt",
		Content: "hello home",
	})
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(home, "homefile.txt"), out.Path)

	written, err := os.ReadFile(out.Path)
	require.NoError(t, err)
	assert.Equal(t, "hello home", string(written))
}

func TestWriteFile_ReplaceExisting(t *testing.T) {
	t.Parallel()

	original := []byte("original content")
	originalHash := hashBytes(original)

	testCases := []struct {
		name    string
		setup   func(t *testing.T, exec *Executor, path string)
		hash    string
		wantErr error
	}{
		{
			name: "success with matching hash",
			setup: func(t *testing.T, exec *Executor, path string) {
				t.Helper()
				exec.observeContent(path, originalHash)
			},
			hash: originalHash,
		},
		{
			name:    "missing observation",
			setup:   func(_ *testing.T, _ *Executor, _ string) {},
			hash:    originalHash,
			wantErr: ErrNotObserved,
		},
		{
			name: "missing hash",
			setup: func(t *testing.T, exec *Executor, path string) {
				t.Helper()
				exec.observeContent(path, originalHash)
			},
			hash:    "",
			wantErr: ErrHashRequired,
		},
		{
			name: "stale hash",
			setup: func(t *testing.T, exec *Executor, path string) {
				t.Helper()
				exec.observeContent(path, originalHash)
			},
			hash:    hashBytes([]byte("something else")),
			wantErr: ErrStaleHash,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			exec := newWriteFileTestExecutor(t, Limits{})
			path := filepath.Join(exec.Workspace(), "existing.txt")
			require.NoError(t, os.WriteFile(path, original, newFileMode))

			tc.setup(t, exec, path)

			out, err := exec.WriteFile(context.Background(), WriteFileInput{
				Path:           path,
				Content:        "replacement content",
				ExpectedSHA256: tc.hash,
			})

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)

				return
			}

			require.NoError(t, err)
			assert.False(t, out.Created)

			written, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, "replacement content", string(written))
		})
	}
}

func TestWriteFile_RejectsNonRegularExistingPath(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		setup func(t *testing.T, exec *Executor) string
	}{
		{
			name: "existing directory",
			setup: func(t *testing.T, exec *Executor) string {
				t.Helper()

				dir := filepath.Join(exec.Workspace(), "adir")
				require.NoError(t, os.MkdirAll(dir, newDirectoryMode))

				return dir
			},
		},
		{
			name: "symlink to a directory",
			setup: func(t *testing.T, exec *Executor) string {
				t.Helper()

				dir := filepath.Join(exec.Workspace(), "linked-dir")
				require.NoError(t, os.MkdirAll(dir, newDirectoryMode))

				link := filepath.Join(exec.Workspace(), "dir-link")
				require.NoError(t, os.Symlink(dir, link))

				return link
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			exec := newWriteFileTestExecutor(t, Limits{})
			path := tc.setup(t, exec)

			_, err := exec.WriteFile(context.Background(), WriteFileInput{
				Path:    path,
				Content: "new content",
			})
			require.ErrorIs(t, err, ErrNotRegularFile)
		})
	}
}

// A symlink to a regular file is a regular file to write to. The replacement
// must land on the target and leave the link in place, which is what writing
// to that path normally does.
func TestWriteFile_WritesThroughSymlinkToTarget(t *testing.T) {
	t.Parallel()

	exec := newWriteFileTestExecutor(t, Limits{})

	target := filepath.Join(exec.Workspace(), "target.txt")
	require.NoError(t, os.WriteFile(target, []byte("data"), newFileMode))

	link := filepath.Join(exec.Workspace(), "target-link.txt")
	require.NoError(t, os.Symlink(target, link))

	_, err := exec.ReadFile(context.Background(), ReadFileInput{Path: link})
	require.NoError(t, err)

	out, err := exec.WriteFile(context.Background(), WriteFileInput{
		Path:           link,
		Content:        "new content",
		ExpectedSHA256: hashBytes([]byte("data")),
	})
	require.NoError(t, err)
	assert.False(t, out.Created)

	written, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "new content", string(written))

	info, err := os.Lstat(link)
	require.NoError(t, err)
	assert.NotZero(
		t,
		info.Mode()&os.ModeSymlink,
		"the link must survive the write",
	)
}

func TestWriteFile_ValidationAndBounds(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		input   func(exec *Executor) WriteFileInput
		wantErr error
	}{
		{
			name: "empty path",
			input: func(_ *Executor) WriteFileInput {
				return WriteFileInput{Content: "x"}
			},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "creation rejects supplied hash",
			input: func(_ *Executor) WriteFileInput {
				return WriteFileInput{
					Path:           "missing.txt",
					Content:        "x",
					ExpectedSHA256: hashBytes([]byte("x")),
				}
			},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "content exceeds write bound",
			input: func(_ *Executor) WriteFileInput {
				return WriteFileInput{
					Path:    "bound.txt",
					Content: strings.Repeat("x", writeTestSmallBound+1),
				}
			},
			wantErr: ErrLimitExceeded,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			exec := newWriteFileTestExecutor(
				t,
				Limits{MaxWriteBytes: writeTestSmallBound},
			)

			_, err := exec.WriteFile(context.Background(), tc.input(exec))
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestWriteFile_CancelledContext(t *testing.T) {
	t.Parallel()

	exec := newWriteFileTestExecutor(t, Limits{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := exec.WriteFile(ctx, WriteFileInput{
		Path:    "whatever.txt",
		Content: "x",
	})
	require.ErrorIs(t, err, context.Canceled)
}

func TestWriteFile_ModePreservation(t *testing.T) {
	t.Parallel()

	exec := newWriteFileTestExecutor(t, Limits{})
	path := filepath.Join(exec.Workspace(), "moded.txt")

	require.NoError(t, os.WriteFile(path, []byte("old"), newFileMode))
	require.NoError(t, os.Chmod(path, writeTestExistingMode))

	hash := hashBytes([]byte("old"))
	exec.observeContent(path, hash)

	_, err := exec.WriteFile(context.Background(), WriteFileInput{
		Path:           path,
		Content:        "new",
		ExpectedSHA256: hash,
	})
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, writeTestExistingMode, info.Mode().Perm())
}

func TestWriteFile_UnicodeAndCRLFContent(t *testing.T) {
	t.Parallel()

	content := "héllo wörld 日本語\r\nsecond line\r\n" //nolint:gosmopolitan // non-ASCII text is the point of this test

	exec := newWriteFileTestExecutor(t, Limits{})

	out, err := exec.WriteFile(context.Background(), WriteFileInput{
		Path:    "unicode.txt",
		Content: content,
	})
	require.NoError(t, err)

	written, err := os.ReadFile(out.Path)
	require.NoError(t, err)
	assert.Equal(t, content, string(written))
	assert.Equal(t, len(content), out.Bytes)
}

func TestWriteFile_AtomicFailureLeavesOriginalIntact(t *testing.T) {
	t.Parallel()

	exec := newWriteFileTestExecutor(t, Limits{})

	protectedDir := filepath.Join(exec.Workspace(), "protected")
	require.NoError(t, os.MkdirAll(protectedDir, newDirectoryMode))

	path := filepath.Join(protectedDir, "existing.txt")
	original := []byte("original content")
	require.NoError(t, os.WriteFile(path, original, newFileMode))

	hash := hashBytes(original)
	exec.observeContent(path, hash)

	require.NoError(t, os.Chmod(protectedDir, writeTestReadOnlyDirMode))
	t.Cleanup(func() {
		_ = os.Chmod(protectedDir, newDirectoryMode)
	})

	// Only the accessible failure surface (permission denied creating the
	// same-directory temp file) can be induced from a black-box test without
	// touching fsutil.go, which this lane does not own.
	_, err := exec.WriteFile(context.Background(), WriteFileInput{
		Path:           path,
		Content:        "new content",
		ExpectedSHA256: hash,
	})
	require.Error(t, err)

	require.NoError(t, os.Chmod(protectedDir, newDirectoryMode))

	remaining, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, remaining)

	entries, err := os.ReadDir(protectedDir)
	require.NoError(t, err)

	for _, entry := range entries {
		assert.False(t, strings.HasPrefix(entry.Name(), temporaryFilePrefix))
	}
}

func TestWriteFile_ObservesNewContentForFollowUpWrite(t *testing.T) {
	t.Parallel()

	exec := newWriteFileTestExecutor(t, Limits{})

	created, err := exec.WriteFile(context.Background(), WriteFileInput{
		Path:    "chained.txt",
		Content: "first",
	})
	require.NoError(t, err)
	assert.True(t, exec.Observed(created.Path))

	replaced, err := exec.WriteFile(context.Background(), WriteFileInput{
		Path:           created.Path,
		Content:        "second",
		ExpectedSHA256: created.SHA256,
	})
	require.NoError(t, err)
	assert.False(t, replaced.Created)

	written, err := os.ReadFile(created.Path)
	require.NoError(t, err)
	assert.Equal(t, "second", string(written))
}

func TestWriteFile_ConcurrentWritesSerialize(t *testing.T) {
	t.Parallel()

	exec := newWriteFileTestExecutor(t, Limits{})

	var wg sync.WaitGroup

	errs := make([]error, 2)
	names := []string{"concurrent-a.txt", "concurrent-b.txt"}

	for i, name := range names {
		wg.Add(1)

		go func(index int, fileName string) {
			defer wg.Done()

			_, err := exec.WriteFile(context.Background(), WriteFileInput{
				Path:    fileName,
				Content: "concurrent",
			})
			errs[index] = err
		}(i, name)
	}

	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}

	for _, name := range names {
		content, err := os.ReadFile(filepath.Join(exec.Workspace(), name))
		require.NoError(t, err)
		assert.Equal(t, "concurrent", string(content))
	}
}
