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

// An unset value confines sessions to the directory Peen was started in, which
// is the single-workspace behavior this setting generalizes.
func TestWorkspaceRootsDefaultsToTheWorkingDirectory(t *testing.T) {
	t.Parallel()

	working := canonicalDir(t)

	roots, err := Config{WorkingDirectory: working}.WorkspaceRoots()
	require.NoError(t, err)
	assert.Equal(t, []string{working}, roots)
}

func TestWorkspaceRootsReadsConfiguredPaths(t *testing.T) {
	t.Parallel()

	base := canonicalDir(t)
	first := filepath.Join(base, "first")
	second := filepath.Join(base, "second")

	require.NoError(t, os.MkdirAll(first, testDirectoryMode))
	require.NoError(t, os.MkdirAll(second, testDirectoryMode))

	config := Config{
		WorkingDirectory:   base,
		WorkspaceRootsJSON: `["` + first + `","` + second + `"]`,
	}

	roots, err := config.WorkspaceRoots()
	require.NoError(t, err)
	assert.Equal(t, []string{first, second}, roots)
}

// Two names for one directory are one root, so the resolved list holds it once.
func TestWorkspaceRootsDeduplicatesResolvedPaths(t *testing.T) {
	t.Parallel()

	base := canonicalDir(t)
	target := filepath.Join(base, "project")
	require.NoError(t, os.MkdirAll(target, testDirectoryMode))

	alias := filepath.Join(base, "alias")
	require.NoError(t, os.Symlink(target, alias))

	config := Config{
		WorkingDirectory:   base,
		WorkspaceRootsJSON: `["` + target + `","` + alias + `"]`,
	}

	roots, err := config.WorkspaceRoots()
	require.NoError(t, err)
	assert.Equal(t, []string{target}, roots)
}

func TestWorkspaceRootsRejectsUnusableValues(t *testing.T) {
	t.Parallel()

	base := canonicalDir(t)

	testCases := []struct {
		name    string
		json    string
		wantErr error
	}{
		{
			name:    "malformed JSON",
			json:    `["/srv/work"`,
			wantErr: nil,
		},
		{
			name:    "relative path",
			json:    `["relative/path"]`,
			wantErr: ErrInvalidConfig,
		},
		{
			name:    "empty array",
			json:    `[]`,
			wantErr: ErrInvalidConfig,
		},
		{
			name:    "missing directory",
			json:    `["` + filepath.Join(base, "absent") + `"]`,
			wantErr: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			config := Config{
				WorkingDirectory:   base,
				WorkspaceRootsJSON: tc.json,
			}

			roots, err := config.WorkspaceRoots()
			require.Error(t, err)
			assert.Nil(t, roots)

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			}
		})
	}
}
