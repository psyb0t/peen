package envtemplate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const envFileName = ".env"

func TestWriteNewWritesAPrivateFile(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, envFileName)

	require.NoError(t, WriteNew(t.Context(), path, fullValues()))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, FileMode, info.Mode().Perm())

	written, err := os.ReadFile(path)
	require.NoError(t, err)

	rendered, err := Render(fullValues())
	require.NoError(t, err)
	assert.Equal(t, rendered, written)

	assertOnlyEntries(t, directory, envFileName)
}

// An operator's existing .env holds their API token and provider keys, so
// writing over it is never acceptable. The message is the contract an
// installer shows, so it must name the file and say it was kept.
func TestWriteNewLeavesAnExistingFileUnchanged(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, envFileName)
	original := []byte("PEEN_API_TOKEN=keep-me\n")
	require.NoError(t, os.WriteFile(path, original, FileMode))

	err := WriteNew(t.Context(), path, fullValues())
	require.ErrorIs(t, err, commerr.ErrAlreadyExists)
	assert.Contains(t, err.Error(), path)
	assert.Contains(t, err.Error(), "left unchanged")

	kept, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, kept)

	assertOnlyEntries(t, directory, envFileName)
}

// A dangling symlink at the path still counts as existing. Writing through it
// would create a file wherever it points.
func TestWriteNewRefusesADanglingSymlink(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, envFileName)
	require.NoError(t, os.Symlink(filepath.Join(directory, "absent"), path))

	err := WriteNew(t.Context(), path, fullValues())
	require.ErrorIs(t, err, commerr.ErrAlreadyExists)

	assertOnlyEntries(t, directory, envFileName)
}

func TestWriteNewRefusesInvalidValuesAndWritesNothing(t *testing.T) {
	t.Parallel()

	for _, tc := range invalidValueCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			directory := t.TempDir()
			path := filepath.Join(directory, envFileName)

			err := WriteNew(t.Context(), path, tc.values)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)

			assertOnlyEntries(t, directory)
		})
	}
}

func TestWriteNewRefusesAnUnusableEnvFilePath(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		path string
	}{
		{name: "relative", path: envFileName},
		{name: "line feed", path: "/absolute/.env\nPEEN_X=1"},
		{name: "carriage return", path: "/absolute/.env\r"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := WriteNew(t.Context(), tc.path, fullValues())
			require.ErrorIs(t, err, commerr.ErrValidationFailed)

			_, statErr := os.Lstat(tc.path)
			require.ErrorIs(t, statErr, os.ErrNotExist)
		})
	}
}

func TestWriteNewFailsInAMissingDirectory(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "absent", envFileName)

	err := WriteNew(t.Context(), path, Values{})
	require.ErrorIs(t, err, os.ErrNotExist)
}

// assertOnlyEntries fails when directory holds anything but want, which is
// how a test sees a leftover temporary file or a partial write.
func assertOnlyEntries(t *testing.T, directory string, want ...string) {
	t.Helper()

	entries, err := os.ReadDir(directory)
	require.NoError(t, err)

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	assert.ElementsMatch(t, want, names)
}
