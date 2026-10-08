package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// canonicalDir resolves a temporary directory through its symlinks, because
// WorkspaceRoots returns resolved paths.
func canonicalDir(t *testing.T) string {
	t.Helper()

	canonical, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	return canonical
}

// An unset value confines sessions to the directory Peen was started in.
func TestWorkspaceRootsDefaultsToTheWorkingDirectory(t *testing.T) {
	t.Parallel()

	working := canonicalDir(t)

	roots, err := Config{WorkingDirectory: working}.WorkspaceRoots()
	require.NoError(t, err)
	assert.Equal(t, []string{working}, roots)
}

func TestWorkspaceRootsReadsTheConfiguredRoot(t *testing.T) {
	t.Parallel()

	base := canonicalDir(t)
	root := filepath.Join(base, "projects")
	require.NoError(t, os.MkdirAll(root, testDirectoryMode))

	testCases := []struct {
		name  string
		value string
	}{
		{name: "plain path", value: root},
		{name: "surrounding whitespace", value: "  " + root + "\t"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			config := Config{WorkingDirectory: base, WorkspaceRoot: tc.value}

			roots, err := config.WorkspaceRoots()
			require.NoError(t, err)
			assert.Equal(t, []string{root}, roots)
		})
	}
}

// A symlinked root resolves to its target, so a request naming either path
// compares equal to the one allowed root.
func TestWorkspaceRootsResolvesSymlinks(t *testing.T) {
	t.Parallel()

	base := canonicalDir(t)
	target := filepath.Join(base, "projects")
	require.NoError(t, os.MkdirAll(target, testDirectoryMode))

	alias := filepath.Join(base, "alias")
	require.NoError(t, os.Symlink(target, alias))

	roots, err := Config{WorkingDirectory: base, WorkspaceRoot: alias}.WorkspaceRoots()
	require.NoError(t, err)
	assert.Equal(t, []string{target}, roots)
}

func TestWorkspaceRootsRejectsUnusableValues(t *testing.T) {
	t.Parallel()

	base := canonicalDir(t)

	testCases := []struct {
		name    string
		value   string
		wantErr error
	}{
		{name: "relative path", value: "relative/path", wantErr: ErrInvalidConfig},
		{name: "dot path", value: ".", wantErr: ErrInvalidConfig},
		{name: "missing directory", value: filepath.Join(base, "absent")},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			config := Config{WorkingDirectory: base, WorkspaceRoot: tc.value}

			roots, err := config.WorkspaceRoots()
			require.Error(t, err)
			assert.Nil(t, roots)

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			}
		})
	}
}

// The removed multi-root list must stop startup with a message naming its
// replacement, rather than being ignored while sessions fall back to the
// working directory.
func TestValidateRefusesTheRemovedWorkspaceRootsList(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		removed string
		wantErr bool
	}{
		{name: "unset", removed: "", wantErr: false},
		{name: "whitespace only", removed: "   ", wantErr: false},
		{name: "a JSON list", removed: `["/srv/work"]`, wantErr: true},
		{name: "an empty JSON list", removed: `[]`, wantErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := Config{RemovedWorkspaceRootsJSON: tc.removed}.validateWorkspaceRoot()
			if !tc.wantErr {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, ErrInvalidConfig)
			assert.Contains(t, err.Error(), "PEEN_WORKSPACE_ROOT")
		})
	}
}
