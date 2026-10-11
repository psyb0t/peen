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
		require.ErrorIs(t, err, commerr.ErrNotFound)
	})
}

func TestWorkspacePolicyRefusesMissingPathOutsideRoot(t *testing.T) {
	t.Parallel()

	base := canonicalTempDir(t)
	root := filepath.Join(base, "allowed")
	rejected := filepath.Join(base, "outside", "absent")
	require.NoError(t, os.MkdirAll(root, 0o750))

	policy, err := control.NewWorkspacePolicy([]string{root})
	require.NoError(t, err)

	resolved, err := policy.Resolve(rejected)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	assert.Empty(t, resolved)
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

// protectedLayout is a workspace root that holds the controller's own
// directories, the way an install with its root at $HOME and its state under
// ~/.config/peen does.
type protectedLayout struct {
	root          string
	home          string
	configParent  string
	state         string
	configuration string
	sockets       string
}

func newProtectedLayout(t *testing.T) protectedLayout {
	t.Helper()

	root := canonicalTempDir(t)
	home := filepath.Join(root, "home")
	configParent := filepath.Join(home, ".config", "peen")

	layout := protectedLayout{
		root:          root,
		home:          home,
		configParent:  configParent,
		state:         filepath.Join(configParent, "state"),
		configuration: filepath.Join(configParent, "config"),
		// The socket root does not exist when the policy is built. Peen
		// creates it when the first worker starts.
		sockets: filepath.Join(root, "run", "peen-workers"),
	}

	for _, directory := range []string{
		filepath.Join(layout.state, "logs"),
		filepath.Join(layout.configuration, ".agents"),
		filepath.Join(home, "project"),
		filepath.Join(configParent, "state-other"),
	} {
		require.NoError(t, os.MkdirAll(directory, 0o750))
	}

	return layout
}

func (l protectedLayout) policy(t *testing.T) *control.WorkspacePolicy {
	t.Helper()

	policy, err := control.NewWorkspacePolicy(
		[]string{l.root},
		control.WithProtectedDirectories(control.ProtectedDirectories{
			State:         []string{l.state, l.sockets},
			Configuration: []string{l.configuration},
		}),
	)
	require.NoError(t, err)

	return policy
}

// A workspace bind-mounts into a Docker worker. A workspace that is, holds, or
// sits inside the state directory would hand that worker peen.db with every
// session's transcript and every session's worker socket.
func TestWorkspacePolicyRefusesWorkspacesExposingControllerDirectories(
	t *testing.T,
) {
	t.Parallel()

	layout := newProtectedLayout(t)
	policy := layout.policy(t)

	testCases := []struct {
		name      string
		requested string
	}{
		{name: "equals the state directory", requested: layout.state},
		{name: "contains the state directory", requested: layout.configParent},
		{
			name:      "contains it two levels up",
			requested: filepath.Join(layout.home, ".config"),
		},
		{name: "is the home holding it", requested: layout.home},
		{name: "is the workspace root", requested: layout.root},
		{
			name:      "sits inside the state directory",
			requested: filepath.Join(layout.state, "logs"),
		},
		{
			name:      "equals the configuration directory",
			requested: layout.configuration,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resolved, err := policy.Resolve(tc.requested)
			require.ErrorIs(t, err, commerr.ErrPermissionDenied)
			assert.Empty(t, resolved)
			assert.NotContains(t, err.Error(), layout.state)
			assert.NotContains(t, err.Error(), layout.configuration)
		})
	}
}

// Resolving the request first is what stops an alias from reaching the state
// directory under another name.
func TestWorkspacePolicyRefusesASymlinkAliasOfTheStateDirectory(
	t *testing.T,
) {
	t.Parallel()

	layout := newProtectedLayout(t)
	policy := layout.policy(t)

	alias := filepath.Join(layout.home, "state-alias")
	require.NoError(t, os.Symlink(layout.state, alias))

	resolved, err := policy.Resolve(alias)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	assert.Empty(t, resolved)
}

// The protected directories are resolved when the policy is built, so a
// deployment that names its state directory through a symlink still has the
// real directory protected.
func TestWorkspacePolicyProtectsTheTargetOfASymlinkedStateDirectory(
	t *testing.T,
) {
	t.Parallel()

	layout := newProtectedLayout(t)

	configuredState := filepath.Join(layout.root, "state-link")
	require.NoError(t, os.Symlink(layout.state, configuredState))

	policy, err := control.NewWorkspacePolicy(
		[]string{layout.root},
		control.WithProtectedDirectories(control.ProtectedDirectories{
			State: []string{configuredState},
		}),
	)
	require.NoError(t, err)

	for _, requested := range []string{layout.state, layout.configParent} {
		resolved, err := policy.Resolve(requested)
		require.ErrorIs(t, err, commerr.ErrPermissionDenied, requested)
		assert.Empty(t, resolved)
	}
}

// A protected directory created after startup, such as the worker socket root,
// is protected under the path it will have.
func TestWorkspacePolicyProtectsAStateDirectoryCreatedLater(t *testing.T) {
	t.Parallel()

	layout := newProtectedLayout(t)
	policy := layout.policy(t)

	require.NoError(t, os.MkdirAll(
		filepath.Join(layout.sockets, "session"),
		0o750,
	))

	for _, requested := range []string{
		filepath.Dir(layout.sockets),
		layout.sockets,
		filepath.Join(layout.sockets, "session"),
	} {
		resolved, err := policy.Resolve(requested)
		require.ErrorIs(t, err, commerr.ErrPermissionDenied, requested)
		assert.Empty(t, resolved)
	}
}

// The comparison is by path component, so a sibling that shares a name prefix
// with a protected directory stays usable, and so does any directory beside
// them.
func TestWorkspacePolicyAdmitsSiblingsOfControllerDirectories(t *testing.T) {
	t.Parallel()

	layout := newProtectedLayout(t)
	policy := layout.policy(t)

	for _, requested := range []string{
		filepath.Join(layout.home, "project"),
		filepath.Join(layout.configParent, "state-other"),
	} {
		resolved, err := policy.Resolve(requested)
		require.NoError(t, err, requested)
		assert.Equal(t, requested, resolved)
	}
}

func TestNewWorkspacePolicyRejectsARelativeProtectedDirectory(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		directories control.ProtectedDirectories
	}{
		{
			name: "state",
			directories: control.ProtectedDirectories{
				State: []string{"state"},
			},
		},
		{
			name: "configuration",
			directories: control.ProtectedDirectories{
				Configuration: []string{"config"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			policy, err := control.NewWorkspacePolicy(
				[]string{canonicalTempDir(t)},
				control.WithProtectedDirectories(tc.directories),
			)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
			assert.Nil(t, policy)
		})
	}
}

// canonicalTempDir resolves the temporary directory through its symlinks so a
// platform that makes it an alias compares equal to what Resolve returns.
func canonicalTempDir(t *testing.T) string {
	t.Helper()

	canonical, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	return canonical
}
