package control_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewWorkspacePolicyRejectsUnusableRoots(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		roots []string
	}{
		{name: "no roots"},
		{name: "relative root", roots: []string{"relative/path"}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			policy, err := control.NewWorkspacePolicy(tc.roots)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
			assert.Nil(t, policy)
		})
	}
}

func TestWorkspacePolicyResolvesInsideRoot(t *testing.T) {
	t.Parallel()

	root := canonicalTempDir(t)
	nested := filepath.Join(root, "project", "nested")
	require.NoError(t, os.MkdirAll(nested, 0o750))

	policy, err := control.NewWorkspacePolicy([]string{root})
	require.NoError(t, err)

	testCases := []struct {
		name      string
		requested string
	}{
		{name: "root itself", requested: root},
		{name: "child", requested: filepath.Join(root, "project")},
		{name: "grandchild", requested: nested},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resolved, err := policy.Resolve(tc.requested)
			require.NoError(t, err)
			assert.Equal(t, tc.requested, resolved)
		})
	}
}

// A symlink pointing at an allowed directory must resolve to that directory, so
// two names for one workspace open the same session rather than two.
func TestWorkspacePolicyResolvesSymlinkToSamePath(t *testing.T) {
	t.Parallel()

	root := canonicalTempDir(t)
	target := filepath.Join(root, "project")
	require.NoError(t, os.MkdirAll(target, 0o750))

	alias := filepath.Join(root, "alias")
	require.NoError(t, os.Symlink(target, alias))

	policy, err := control.NewWorkspacePolicy([]string{root})
	require.NoError(t, err)

	fromTarget, err := policy.Resolve(target)
	require.NoError(t, err)

	fromAlias, err := policy.Resolve(alias)
	require.NoError(t, err)

	assert.Equal(t, fromTarget, fromAlias)
}

// Resolving before the root check is what stops a symlink inside an allowed
// root from reaching a directory outside it.
func TestWorkspacePolicyRefusesSymlinkEscapingRoot(t *testing.T) {
	t.Parallel()

	base := canonicalTempDir(t)
	root := filepath.Join(base, "allowed")
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.MkdirAll(root, 0o750))
	require.NoError(t, os.MkdirAll(outside, 0o750))

	escape := filepath.Join(root, "escape")
	require.NoError(t, os.Symlink(outside, escape))

	policy, err := control.NewWorkspacePolicy([]string{root})
	require.NoError(t, err)

	resolved, err := policy.Resolve(escape)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	assert.Empty(t, resolved)
}

// A string-prefix containment check would wrongly admit a sibling that shares
// the root's name prefix, and a parent of the root.
func TestWorkspacePolicyRefusesPathsOutsideRoot(t *testing.T) {
	t.Parallel()

	base := canonicalTempDir(t)
	root := filepath.Join(base, "work")
	sibling := filepath.Join(base, "work-other")
	require.NoError(t, os.MkdirAll(root, 0o750))
	require.NoError(t, os.MkdirAll(sibling, 0o750))

	policy, err := control.NewWorkspacePolicy([]string{root})
	require.NoError(t, err)

	testCases := []struct {
		name      string
		requested string
	}{
		{name: "sibling sharing name prefix", requested: sibling},
		{name: "parent of root", requested: base},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resolved, err := policy.Resolve(tc.requested)
			require.ErrorIs(t, err, commerr.ErrPermissionDenied)
			assert.Empty(t, resolved)
		})
	}
}

func TestWorkspacePolicyAdmitsAnyConfiguredRoot(t *testing.T) {
	t.Parallel()

	base := canonicalTempDir(t)
	first := filepath.Join(base, "first")
	second := filepath.Join(base, "second")
	require.NoError(t, os.MkdirAll(first, 0o750))
	require.NoError(t, os.MkdirAll(second, 0o750))

	policy, err := control.NewWorkspacePolicy([]string{first, second})
	require.NoError(t, err)

	for _, requested := range []string{first, second} {
		resolved, err := policy.Resolve(requested)
		require.NoError(t, err)
		assert.Equal(t, requested, resolved)
	}
}

func TestWorkspacePolicyRefusesUnusableRequests(t *testing.T) {
	t.Parallel()

	root := canonicalTempDir(t)

	file := filepath.Join(root, "file.txt")
	require.NoError(t, os.WriteFile(file, []byte("content"), 0o600))

	policy, err := control.NewWorkspacePolicy([]string{root})
	require.NoError(t, err)

	t.Run("blank workspace", func(t *testing.T) {
		t.Parallel()

		_, err := policy.Resolve("  ")
		require.ErrorIs(t, err, commerr.ErrValidationFailed)
	})

	t.Run("file is not a directory", func(t *testing.T) {
		t.Parallel()

		_, err := policy.Resolve(file)
		require.ErrorIs(t, err, commerr.ErrValidationFailed)
	})

	t.Run("missing directory", func(t *testing.T) {
		t.Parallel()

		_, err := policy.Resolve(filepath.Join(root, "absent"))
		require.Error(t, err)
	})
}

func TestWorkspacePolicyRootsAreNotAliasedToCaller(t *testing.T) {
	t.Parallel()

	root := canonicalTempDir(t)

	policy, err := control.NewWorkspacePolicy([]string{root})
	require.NoError(t, err)

	reported := policy.Roots()
	require.Len(t, reported, 1)
	reported[0] = filepath.Join(root, "mutated")

	assert.Equal(t, []string{root}, policy.Roots())
}

// canonicalTempDir resolves the temporary directory through its symlinks so a
// platform that makes it an alias compares equal to what Resolve returns.
func canonicalTempDir(t *testing.T) string {
	t.Helper()

	canonical, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	return canonical
}
