package workspacefs

import (
	"io"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryFilesystemContract(t *testing.T) {
	t.Parallel()
	files := map[string]string{"a.txt": "alpha", "nested/b.txt": "beta"}
	memory, err := NewMemory(files)
	require.NoError(t, err)
	require.NoError(t, fstest.TestFS(memory, "a.txt", "nested/b.txt"))
	files["a.txt"] = "modified caller map"
	data, err := fs.ReadFile(memory, "a.txt")
	require.NoError(t, err)
	assert.Equal(t, "alpha", string(data))
	root, err := memory.Open(".")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	directory, ok := root.(fs.ReadDirFile)
	require.True(t, ok)
	entries, err := directory.ReadDir(1)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "a.txt", entries[0].Name())
	entries, err = directory.ReadDir(1)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "nested", entries[0].Name())
	_, err = directory.ReadDir(1)
	require.ErrorIs(t, err, io.EOF)
}

func TestMemoryRejectsInvalidAndConflictingPaths(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		files map[string]string
	}{
		{name: "absolute", files: map[string]string{"/a": "content"}},
		{name: "traversal", files: map[string]string{"../a": "content"}},
		{name: "root file", files: map[string]string{".": "content"}},
		{name: "empty path", files: map[string]string{"": "content"}},
		{name: "file parent", files: map[string]string{"a": "file", "a/b": "nested"}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewMemory(tc.files)
			require.ErrorIs(t, err, fs.ErrInvalid)
		})
	}
}

func TestWalkSkipsSiblingFilesAndNestedDirectories(t *testing.T) {
	t.Parallel()
	memory, err := NewMemory(map[string]string{
		"a.txt": "a", "b.txt": "b", "nested/c.txt": "c",
	})
	require.NoError(t, err)
	reader := ReadOnly{Source: memory}
	var visited []string
	err = WalkDir(reader, ".", func(name string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		visited = append(visited, name)
		if name == "a.txt" {
			return fs.SkipDir
		}

		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{".", "a.txt"}, visited)
	visited = nil
	err = WalkDir(reader, ".", func(name string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		visited = append(visited, name)
		if name == "nested" {
			return fs.SkipDir
		}

		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{".", "a.txt", "b.txt", "nested"}, visited)
}
