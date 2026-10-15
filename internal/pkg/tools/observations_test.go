package tools

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/workspacefs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestObservationSnapshotRestoresReadProofForAnotherExecutor(t *testing.T) {
	t.Parallel()
	executor, directory := newEditTestExecutor(t, Limits{})
	path := writeEditFixture(t, directory, "example.txt", "old content")
	_, err := executor.ReadFile(t.Context(), ReadFileInput{Path: path})
	require.NoError(t, err)
	encoded, err := json.Marshal(executor.SnapshotObservations())
	require.NoError(t, err)
	var snapshot []ObservedPath
	require.NoError(t, json.Unmarshal(encoded, &snapshot))
	resumed, _ := newEditTestExecutor(t, Limits{})
	require.NoError(t, resumed.RestoreObservations(snapshot))
	_, err = resumed.EditFile(t.Context(), EditFileInput{
		Path: path, Edits: []TextEdit{{Old: "old", New: "new"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "new content", string(readFixtureContent(t, path)))
	assert.Equal(t, hashBytes([]byte("new content")),
		resumed.SnapshotObservations()[0].Hash)
}

func TestRestoredObservationStillRejectsChangedContent(t *testing.T) {
	t.Parallel()
	executor, directory := newEditTestExecutor(t, Limits{})
	path := writeEditFixture(t, directory, "example.txt", "old content")
	_, err := executor.ReadFile(t.Context(), ReadFileInput{Path: path})
	require.NoError(t, err)
	resumed, _ := newEditTestExecutor(t, Limits{})
	require.NoError(t, resumed.RestoreObservations(executor.SnapshotObservations()))
	require.NoError(t, os.WriteFile(path, []byte("changed content"), testFileMode))
	_, err = resumed.EditFile(t.Context(), EditFileInput{
		Path: path, Edits: []TextEdit{{Old: "changed", New: "new"}},
	})
	require.ErrorIs(t, err, ErrStaleHash)
	assert.Equal(t, "changed content", string(readFixtureContent(t, path)))
}

func TestRestoredObservationsContinueReadRevision(t *testing.T) {
	t.Parallel()
	executor, directory := newEditTestExecutor(t, Limits{})
	path := writeEditFixture(t, directory, "example.txt", "old content")
	_, err := executor.ReadFile(t.Context(), ReadFileInput{Path: path})
	require.NoError(t, err)
	first := executor.SnapshotObservations()
	fork, err := executor.AtDirectory(t.Context(), directory)
	require.NoError(t, err)
	resumed, _ := newEditTestExecutor(t, Limits{})
	require.NoError(t, resumed.RestoreObservations(first))
	for _, current := range []*Executor{fork, resumed} {
		_, err = current.ReadFile(t.Context(), ReadFileInput{Path: path})
		require.NoError(t, err)
		assert.Greater(t, current.SnapshotObservations()[0].Revision,
			first[0].Revision)
	}
}

func TestObservationRevisionDoesNotWrap(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		revision uint64
	}{
		{name: "last increment", revision: math.MaxUint64 - 1},
		{name: "saturated", revision: math.MaxUint64},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			executor, directory := newEditTestExecutor(t, Limits{})
			path := writeEditFixture(t, directory, "example.txt", "content")
			require.NoError(t, executor.RestoreObservations([]ObservedPath{{
				Path: path, Revision: tc.revision,
			}}))
			_, err := executor.ReadFile(t.Context(), ReadFileInput{Path: path})
			require.NoError(t, err)
			assert.Equal(t, uint64(math.MaxUint64),
				executor.SnapshotObservations()[0].Revision)
		})
	}
}

func TestObservationSnapshotHasStableOrderAndNoSharedState(t *testing.T) {
	t.Parallel()
	executor, directory := newEditTestExecutor(t, Limits{})
	first, second := filepath.Join(directory, "a"), filepath.Join(directory, "z")
	executor.observe(second)
	executor.observe(first)
	snapshot := executor.SnapshotObservations()
	assert.Equal(t, []ObservedPath{
		{Path: first, Revision: 2}, {Path: second, Revision: 1},
	}, snapshot)
	snapshot[0].Path = "changed"
	assert.Equal(t, first, executor.SnapshotObservations()[0].Path)
	require.NoError(t, executor.RestoreObservations(nil))
	assert.Empty(t, executor.SnapshotObservations())
}

func TestRestoreObservationsRejectsWholeInvalidSnapshot(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		entry ObservedPath
	}{
		{name: "empty path"},
		{name: "relative", entry: ObservedPath{Path: "example"}},
		{name: "unclean", entry: ObservedPath{Path: "/example/../file"}},
		{name: "hash without read", entry: ObservedPath{
			Path: "/example", Hash: "unexpected",
		}},
		{name: "missing hash", entry: ObservedPath{
			Path: "/example", HasHash: true,
		}},
		{name: "bad hex", entry: ObservedPath{
			Path: "/example", HasHash: true, Hash: strings.Repeat("z", 64),
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			executor, directory := newEditTestExecutor(t, Limits{})
			executor.observe(directory)
			before := executor.SnapshotObservations()
			err := executor.RestoreObservations([]ObservedPath{
				{Path: "/valid"}, tc.entry,
			})
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
			assert.Equal(t, before, executor.SnapshotObservations())
		})
	}
}

func TestRestoreObservationsRefusesDuplicatePaths(t *testing.T) {
	t.Parallel()
	executor, directory := newEditTestExecutor(t, Limits{})
	executor.observe(directory)
	before := executor.SnapshotObservations()
	err := executor.RestoreObservations([]ObservedPath{
		{Path: "/duplicate"}, {Path: "/duplicate"},
	})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	assert.Equal(t, before, executor.SnapshotObservations())
}

func TestRestoreVirtualObservationsUsesVirtualPaths(t *testing.T) {
	t.Parallel()
	filesystem := workspacefs.ReadOnly{Source: fstest.MapFS{
		"nested/example.txt": {Data: []byte("content")},
	}}
	executor, err := NewExecutor(Options{
		Workspace: "nested", Filesystem: filesystem, Virtual: true,
	})
	require.NoError(t, err)
	require.NoError(t, executor.RestoreObservations([]ObservedPath{
		{
			Path: "nested/example.txt", HasHash: true,
			Hash: hashBytes([]byte("content")),
		},
	}))
	assert.True(t, executor.Observed("nested/example.txt"))
	err = executor.RestoreObservations([]ObservedPath{{Path: "/absolute"}})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	assert.True(t, executor.Observed("nested/example.txt"))
}
