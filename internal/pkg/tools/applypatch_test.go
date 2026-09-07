package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const applyPatchTestMode = os.FileMode(0o600)

func newApplyPatchTestExecutor(t *testing.T, limits Limits) (*Executor, string) {
	t.Helper()

	workspace := t.TempDir()
	executor, err := NewExecutor(Options{Workspace: workspace, Limits: limits})
	require.NoError(t, err)

	return executor, workspace
}

func writeObservedPatchFile(
	t *testing.T,
	executor *Executor,
	path string,
	content string,
) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), newDirectoryMode))
	require.NoError(t, os.WriteFile(path, []byte(content), applyPatchTestMode))
	executor.observeContent(path, hashBytes([]byte(content)))
}

func applyPatchText(body string) string {
	return applyPatchBeginMarker + "\n" + body + "\n" + applyPatchEndMarker
}

func TestApplyPatchPathsReportsSourcesAndDestinationsInDocumentOrder(t *testing.T) {
	t.Parallel()

	paths, err := ApplyPatchPaths(applyPatchText(strings.Join([]string{
		"*** Update File: update.txt",
		"@@",
		"-before",
		"+after",
		"*** Add File: added.txt",
		"+added",
		"*** Delete File: deleted.txt",
		"*** Update File: source.txt",
		"*** Move to: destination.txt",
		"@@",
		"-source",
		"+destination",
	}, "\n")))
	require.NoError(t, err)
	assert.Equal(t, []string{
		"update.txt",
		"added.txt",
		"deleted.txt",
		"source.txt",
		"destination.txt",
	}, paths)

	_, err = ApplyPatchPaths("not a patch")
	require.ErrorIs(t, err, ErrInvalidPatch)
}

func TestExecutor_ApplyPatch_AllOperations(t *testing.T) {
	t.Parallel()

	executor, workspace := newApplyPatchTestExecutor(t, Limits{})
	updatePath := filepath.Join(workspace, "update.txt")
	deletePath := filepath.Join(workspace, "delete.txt")
	movePath := filepath.Join(workspace, "move.txt")
	moveDestination := filepath.Join(workspace, "nested", "moved.txt")
	addPath := filepath.Join(workspace, "nested", "added.txt")

	writeObservedPatchFile(t, executor, updatePath, "alpha\nbeta\ngamma\n")
	writeObservedPatchFile(t, executor, deletePath, "delete me\n")
	writeObservedPatchFile(t, executor, movePath, "before move\n")

	output, err := executor.ApplyPatch(context.Background(), ApplyPatchInput{
		Patch: applyPatchText(strings.Join([]string{
			"*** Update File: update.txt",
			"@@",
			" alpha",
			"-beta",
			"+changed",
			" gamma",
			"*** Add File: nested/added.txt",
			"+added",
			"+content",
			"*** Delete File: delete.txt",
			"*** Update File: move.txt",
			"*** Move to: nested/moved.txt",
			"@@",
			"-before move",
			"+after move",
		}, "\n")),
	})
	require.NoError(t, err)
	require.Len(t, output.Files, 4)
	assert.Equal(t, PatchOperationUpdate, output.Files[0].Operation)
	assert.Equal(t, PatchOutcomeApplied, output.Files[0].Outcome)
	assert.NotEmpty(t, output.Files[0].SHA256)
	assert.Equal(t, PatchOperationAdd, output.Files[1].Operation)
	assert.Equal(t, PatchOperationDelete, output.Files[2].Operation)
	assert.Equal(t, PatchOperationMove, output.Files[3].Operation)
	assert.Equal(t, moveDestination, output.Files[3].Destination)
	assert.Contains(t, output.Diff, "+changed")

	updated, readErr := os.ReadFile(updatePath)
	require.NoError(t, readErr)
	assert.Equal(t, "alpha\nchanged\ngamma\n", string(updated))
	added, readErr := os.ReadFile(addPath)
	require.NoError(t, readErr)
	assert.Equal(t, "added\ncontent\n", string(added))
	_, statErr := os.Stat(deletePath)
	require.ErrorIs(t, statErr, os.ErrNotExist)
	_, statErr = os.Stat(movePath)
	require.ErrorIs(t, statErr, os.ErrNotExist)
	moved, readErr := os.ReadFile(moveDestination)
	require.NoError(t, readErr)
	assert.Equal(t, "after move\n", string(moved))
}

func TestExecutor_ApplyPatch_PreservesLineEndingsAndMode(t *testing.T) {
	t.Parallel()

	executor, workspace := newApplyPatchTestExecutor(t, Limits{})
	path := filepath.Join(workspace, "script.sh")
	writeObservedPatchFile(t, executor, path, "one\r\ntwo\r\n")
	require.NoError(t, os.Chmod(path, 0o400))

	_, err := executor.ApplyPatch(context.Background(), ApplyPatchInput{
		Patch: strings.ReplaceAll(applyPatchText(strings.Join([]string{
			"*** Update File: script.sh",
			"@@",
			" one",
			"-two",
			"+changed",
		}, "\n")), "\n", "\r\n"),
	})
	require.NoError(t, err)

	content, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, "one\r\nchanged\r\n", string(content))
	info, statErr := os.Stat(path)
	require.NoError(t, statErr)
	assert.Equal(t, os.FileMode(0o400), info.Mode().Perm())
}

func TestExecutor_ApplyPatch_PreservesMissingFinalNewline(t *testing.T) {
	t.Parallel()

	executor, workspace := newApplyPatchTestExecutor(t, Limits{})
	path := filepath.Join(workspace, "note.txt")
	writeObservedPatchFile(t, executor, path, "one\ntwo")

	_, err := executor.ApplyPatch(context.Background(), ApplyPatchInput{
		Patch: applyPatchText(strings.Join([]string{
			"*** Update File: note.txt",
			"@@",
			" one",
			"-two",
			"+changed",
			"*** End of File",
		}, "\n")),
	})
	require.NoError(t, err)

	content, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, "one\nchanged", string(content))
}

func TestExecutor_ApplyPatch_RequiresFreshObservedContent(t *testing.T) {
	t.Parallel()

	executor, workspace := newApplyPatchTestExecutor(t, Limits{})
	path := filepath.Join(workspace, "note.txt")
	require.NoError(t, os.WriteFile(path, []byte("before\n"), applyPatchTestMode))

	patch := ApplyPatchInput{Patch: applyPatchText(strings.Join([]string{
		"*** Update File: note.txt",
		"@@",
		"-before",
		"+after",
	}, "\n"))}

	_, err := executor.ApplyPatch(context.Background(), patch)
	require.ErrorIs(t, err, ErrNotObserved)

	executor.observeContent(path, hashBytes([]byte("before\n")))
	require.NoError(t, os.WriteFile(path, []byte("changed elsewhere\n"), applyPatchTestMode))
	_, err = executor.ApplyPatch(context.Background(), patch)
	require.ErrorIs(t, err, ErrStaleHash)
}

func TestExecutor_ApplyPatch_PrevalidatesEveryAction(t *testing.T) {
	t.Parallel()

	executor, workspace := newApplyPatchTestExecutor(t, Limits{})
	path := filepath.Join(workspace, "existing.txt")
	writeObservedPatchFile(t, executor, path, "actual\n")

	_, err := executor.ApplyPatch(context.Background(), ApplyPatchInput{
		Patch: applyPatchText(strings.Join([]string{
			"*** Add File: should-not-exist.txt",
			"+created",
			"*** Update File: existing.txt",
			"@@",
			"-missing",
			"+replacement",
		}, "\n")),
	})
	require.ErrorIs(t, err, ErrPatchMatchNotFound)

	_, statErr := os.Stat(filepath.Join(workspace, "should-not-exist.txt"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
	content, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, "actual\n", string(content))
}

func TestExecutor_ApplyPatch_RejectsAmbiguousContext(t *testing.T) {
	t.Parallel()

	executor, workspace := newApplyPatchTestExecutor(t, Limits{})
	path := filepath.Join(workspace, "repeated.txt")
	writeObservedPatchFile(t, executor, path, "same\nsame\n")

	_, err := executor.ApplyPatch(context.Background(), ApplyPatchInput{
		Patch: applyPatchText(strings.Join([]string{
			"*** Update File: repeated.txt",
			"@@",
			"-same",
			"+changed",
		}, "\n")),
	})
	require.ErrorIs(t, err, ErrPatchMatchNotUnique)
}

func TestExecutor_ApplyPatch_RejectsBinaryAndDirectorySources(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		setup   func(t *testing.T, executor *Executor, workspace string)
		wantErr error
	}{
		{
			name: "binary file",
			setup: func(t *testing.T, executor *Executor, workspace string) {
				t.Helper()
				writeObservedPatchFile(
					t,
					executor,
					filepath.Join(workspace, "source"),
					"bad\x00data",
				)
			},
			wantErr: ErrBinaryContent,
		},
		{
			name: "directory",
			setup: func(t *testing.T, executor *Executor, workspace string) {
				t.Helper()
				path := filepath.Join(workspace, "source")
				require.NoError(t, os.Mkdir(path, newDirectoryMode))
				executor.observe(path)
			},
			wantErr: ErrNotRegularFile,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			executor, workspace := newApplyPatchTestExecutor(t, Limits{})
			tc.setup(t, executor, workspace)
			_, err := executor.ApplyPatch(context.Background(), ApplyPatchInput{
				Patch: applyPatchText(strings.Join([]string{
					"*** Update File: source",
					"@@",
					"-bad",
					"+good",
				}, "\n")),
			})
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestExecutor_ApplyPatch_RejectsExistingMoveDestination(t *testing.T) {
	t.Parallel()

	executor, workspace := newApplyPatchTestExecutor(t, Limits{})
	source := filepath.Join(workspace, "source.txt")
	destination := filepath.Join(workspace, "destination.txt")
	writeObservedPatchFile(t, executor, source, "source\n")
	require.NoError(t, os.WriteFile(destination, []byte("keep\n"), newFileMode))

	_, err := executor.ApplyPatch(context.Background(), ApplyPatchInput{
		Patch: applyPatchText(strings.Join([]string{
			"*** Update File: source.txt",
			"*** Move to: destination.txt",
		}, "\n")),
	})
	require.ErrorIs(t, err, commerr.ErrAlreadyExists)

	content, readErr := os.ReadFile(destination)
	require.NoError(t, readErr)
	assert.Equal(t, "keep\n", string(content))
}

func TestExecutor_ApplyPatch_RejectsPathConflicts(t *testing.T) {
	t.Parallel()

	executor, _ := newApplyPatchTestExecutor(t, Limits{})
	_, err := executor.ApplyPatch(context.Background(), ApplyPatchInput{
		Patch: applyPatchText(strings.Join([]string{
			"*** Add File: same.txt",
			"+first",
			"*** Add File: same.txt",
			"+second",
		}, "\n")),
	})
	require.ErrorIs(t, err, ErrPatchConflict)
}

func TestExecutor_ApplyPatch_RejectsMalformedDocuments(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		patch string
	}{
		{name: "missing envelope", patch: "*** Add File: one.txt\n+one"},
		{name: "empty patch", patch: applyPatchText("")},
		{name: "unknown action", patch: applyPatchText("*** Copy File: one.txt")},
		{
			name: "invalid add line",
			patch: applyPatchText(strings.Join([]string{
				"*** Add File: one.txt",
				"one",
			}, "\n")),
		},
		{
			name: "context only hunk",
			patch: applyPatchText(strings.Join([]string{
				"*** Update File: one.txt",
				"@@",
				" one",
			}, "\n")),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			executor, _ := newApplyPatchTestExecutor(t, Limits{})
			_, err := executor.ApplyPatch(context.Background(), ApplyPatchInput{
				Patch: tc.patch,
			})
			require.ErrorIs(t, err, ErrInvalidPatch)
		})
	}
}

func TestExecutor_ApplyPatch_AddsEmptyFileWithoutReplacing(t *testing.T) {
	t.Parallel()

	executor, workspace := newApplyPatchTestExecutor(t, Limits{})
	path := filepath.Join(workspace, "empty.txt")

	output, err := executor.ApplyPatch(context.Background(), ApplyPatchInput{
		Patch: applyPatchText("*** Add File: empty.txt"),
	})
	require.NoError(t, err)
	require.Len(t, output.Files, 1)
	assert.Zero(t, output.Files[0].Bytes)

	content, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Empty(t, content)

	_, err = executor.ApplyPatch(context.Background(), ApplyPatchInput{
		Patch: applyPatchText("*** Add File: empty.txt\n+replacement"),
	})
	require.ErrorIs(t, err, commerr.ErrAlreadyExists)

	content, readErr = os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Empty(t, content)
}

func TestExecutor_ApplyPatch_RejectsNonTextPatch(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		patch string
	}{
		{name: "NUL", patch: applyPatchText("*** Add File: x\n+bad\x00")},
		{name: "invalid UTF-8", patch: string([]byte{0xff})},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			executor, _ := newApplyPatchTestExecutor(t, Limits{})
			_, err := executor.ApplyPatch(context.Background(), ApplyPatchInput{
				Patch: tc.patch,
			})
			require.ErrorIs(t, err, ErrInvalidPatch)
		})
	}
}

func TestExecutor_ApplyPatch_EnforcesBounds(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		limits Limits
		patch  string
	}{
		{
			name:   "document bytes",
			limits: Limits{MaxPatchBytes: 1},
			patch:  applyPatchText("*** Add File: one.txt\n+one"),
		},
		{
			name:   "file count",
			limits: Limits{MaxPatchFiles: 1},
			patch: applyPatchText(strings.Join([]string{
				"*** Add File: one.txt",
				"+one",
				"*** Add File: two.txt",
				"+two",
			}, "\n")),
		},
		{
			name:   "changed bytes",
			limits: Limits{MaxPatchChangedBytes: 1},
			patch:  applyPatchText("*** Add File: one.txt\n+too large"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			executor, _ := newApplyPatchTestExecutor(t, tc.limits)
			_, err := executor.ApplyPatch(context.Background(), ApplyPatchInput{
				Patch: tc.patch,
			})
			require.ErrorIs(t, err, ErrLimitExceeded)
		})
	}
}

func TestExecutor_ApplyPatch_BoundsCombinedDiff(t *testing.T) {
	t.Parallel()

	executor, _ := newApplyPatchTestExecutor(t, Limits{MaxDiffBytes: 32})
	output, err := executor.ApplyPatch(context.Background(), ApplyPatchInput{
		Patch: applyPatchText("*** Add File: long.txt\n+abcdefghijklmnopqrstuvwxyz"),
	})
	require.NoError(t, err)
	assert.True(t, output.DiffTruncated)
	assert.Contains(t, output.Diff, diffTruncatedTag)
}

func TestExecutor_ApplyPatch_AllowsPathsOutsideWorkspace(t *testing.T) {
	t.Parallel()

	executor, workspace := newApplyPatchTestExecutor(t, Limits{})
	path := filepath.Join(filepath.Dir(workspace), "outside.txt")
	output, err := executor.ApplyPatch(context.Background(), ApplyPatchInput{
		Patch: applyPatchText("*** Add File: " + path + "\n+outside"),
	})
	require.NoError(t, err)
	require.Len(t, output.Files, 1)
	assert.Equal(t, path, output.Files[0].Path)
}

func TestExecutor_ApplyPatch_RejectsRootDeletion(t *testing.T) {
	t.Parallel()

	for _, path := range []string{".", string(os.PathSeparator)} {
		executor, _ := newApplyPatchTestExecutor(t, Limits{})
		_, err := executor.ApplyPatch(context.Background(), ApplyPatchInput{
			Patch: applyPatchText("*** Delete File: " + path),
		})
		require.ErrorIs(t, err, commerr.ErrValidationFailed)
	}
}

func TestExecutor_ApplyPatch_StopsOnCancellation(t *testing.T) {
	t.Parallel()

	executor, _ := newApplyPatchTestExecutor(t, Limits{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := executor.ApplyPatch(ctx, ApplyPatchInput{
		Patch: applyPatchText("*** Add File: canceled.txt\n+nope"),
	})
	require.True(t, errors.Is(err, context.Canceled))
}

func TestExecutor_ApplyPatch_ConcurrentCallsSerialize(t *testing.T) {
	t.Parallel()

	executor, workspace := newApplyPatchTestExecutor(t, Limits{})
	path := filepath.Join(workspace, "shared.txt")
	writeObservedPatchFile(t, executor, path, "before\n")

	results := make(chan error, 2)
	for _, replacement := range []string{"first", "second"} {
		go func() {
			_, err := executor.ApplyPatch(context.Background(), ApplyPatchInput{
				Patch: applyPatchText(strings.Join([]string{
					"*** Update File: shared.txt",
					"@@",
					"-before",
					"+" + replacement,
				}, "\n")),
			})
			results <- err
		}()
	}

	successes := 0
	failures := 0
	for range 2 {
		if err := <-results; err != nil {
			failures++

			continue
		}

		successes++
	}

	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, failures)
}
