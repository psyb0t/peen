package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenUsesPrivateFilesystemState(t *testing.T) {
	stateDirectory := filepath.Join(t.TempDir(), "state")
	handle, err := Open(context.Background(), Config{Directory: stateDirectory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	directoryInfo, err := os.Stat(stateDirectory)
	require.NoError(t, err)
	assert.Equal(t, sqliteDirectoryMode, directoryInfo.Mode().Perm())

	databaseInfo, err := os.Stat(filepath.Join(stateDirectory, sqliteFileName))
	require.NoError(t, err)
	assert.Equal(t, sqliteFileMode, databaseInfo.Mode().Perm())
}

func TestOpenRejectsSymlinkDirectory(t *testing.T) {
	stateRoot := t.TempDir()
	linkPath := filepath.Join(stateRoot, "state-link")
	require.NoError(t, os.Symlink(filepath.Join(stateRoot, "target"), linkPath))

	handle, err := Open(context.Background(), Config{Directory: linkPath})
	assert.Nil(t, handle)
	require.Error(t, err)
}

func TestOpenRejectsSymlinkDatabase(t *testing.T) {
	stateDirectory := t.TempDir()
	databasePath := filepath.Join(stateDirectory, sqliteFileName)
	require.NoError(t, os.Symlink(filepath.Join(stateDirectory, "target.db"), databasePath))

	handle, err := Open(context.Background(), Config{Directory: stateDirectory})
	assert.Nil(t, handle)
	require.Error(t, err)
}

func TestOpenRestrictsExistingFilesystemModes(t *testing.T) {
	stateDirectory := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.Mkdir(stateDirectory, 0o750))
	databasePath := filepath.Join(stateDirectory, sqliteFileName)
	require.NoError(t, os.WriteFile(databasePath, nil, 0o600))
	require.NoError(t, os.Chmod(stateDirectory, 0o777)) //nolint:gosec // proves Open() tightens an over-permissive existing directory
	require.NoError(t, os.Chmod(databasePath, 0o666))   //nolint:gosec // proves Open() tightens an over-permissive existing file

	handle, err := Open(context.Background(), Config{Directory: stateDirectory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	directoryInfo, err := os.Stat(stateDirectory)
	require.NoError(t, err)
	assert.Equal(t, sqliteDirectoryMode, directoryInfo.Mode().Perm())

	databaseInfo, err := os.Stat(databasePath)
	require.NoError(t, err)
	assert.Equal(t, sqliteFileMode, databaseInfo.Mode().Perm())
}
