package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommandEnvironmentPrependsOnlyTheOperatorBin(t *testing.T) {
	t.Parallel()
	deployment := testConfig(t)
	deployment.ConfigDirectory = t.TempDir()
	directory := filepath.Join(deployment.ConfigDirectory,
		defaultOperatorBinDirectoryPath)
	original := []string{"PATH=/usr/bin", "KEEP=yes"}
	without, err := deployment.CommandEnvironment(original)
	require.NoError(t, err)
	assert.Equal(t, original, without)
	require.NoError(t, os.MkdirAll(directory, 0o700))
	with, err := deployment.CommandEnvironment(original)
	require.NoError(t, err)
	assert.Contains(t, with, "PATH="+directory+":/usr/bin")
	assert.Equal(t, []string{"PATH=/usr/bin", "KEEP=yes"}, original)
	missingPath, err := deployment.CommandEnvironment([]string{"KEEP=yes"})
	require.NoError(t, err)
	assert.Contains(t, missingPath, "PATH="+directory)
}

func TestOperatorBinMustBeADirectory(t *testing.T) {
	t.Parallel()
	deployment := testConfig(t)
	deployment.ConfigDirectory = t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(deployment.ConfigDirectory,
		".agents"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(deployment.ConfigDirectory,
		defaultOperatorBinDirectoryPath), []byte("not a directory"), 0o600))
	directory, err := deployment.OperatorBinDirectory()
	require.NoError(t, err)
	assert.Empty(t, directory)
}
