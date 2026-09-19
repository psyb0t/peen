package control_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testRootAgent    = "peen"
	testDefaultModel = "provider/model"
)

// These tests do not call t.Parallel. db.Open installs the generated
// repositories as a package default, which is process-global state, so two
// fixtures opening a database at once race.
type registryFixture struct {
	registry *control.Registry
	store    *session.Store
	root     string
}

func newRegistryFixture(t *testing.T) registryFixture {
	t.Helper()

	handle, err := db.Open(t.Context(), db.Config{Directory: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, handle.Close())
	})

	store, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)

	root := canonicalTempDir(t)

	policy, err := control.NewWorkspacePolicy([]string{root})
	require.NoError(t, err)

	profiles, err := worker.NewProfileSet(
		[]worker.Profile{{
			Name: worker.ProfileNative,
			Kind: worker.KindNative,
		}},
		worker.ProfileNative,
	)
	require.NoError(t, err)

	registry, err := control.NewRegistry(control.RegistryOptions{
		Store:        store,
		Policy:       policy,
		Profiles:     profiles,
		RootAgent:    testRootAgent,
		DefaultModel: testDefaultModel,
	})
	require.NoError(t, err)

	return registryFixture{registry: registry, store: store, root: root}
}

func (f registryFixture) workspace(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join(f.root, name)
	require.NoError(t, os.MkdirAll(path, 0o750))

	return path
}

func TestRegistryOpenCreatesThenResumesOneSession(t *testing.T) {
	fixture := newRegistryFixture(t)
	workspace := fixture.workspace(t, "project")

	first, err := fixture.registry.Open(t.Context(), workspace, "")
	require.NoError(t, err)
	assert.True(t, first.Created)
	assert.Equal(t, workspace, first.Session.Workspace)

	second, err := fixture.registry.Open(t.Context(), workspace, "")
	require.NoError(t, err)
	assert.False(t, second.Created)
	assert.Equal(t, first.Session.ID, second.Session.ID)
}

// Two names for one directory are one workspace, so they must resolve to the
// same session rather than creating a second one for the same files.
func TestRegistryOpenResolvesSymlinkToTheSameSession(t *testing.T) {
	fixture := newRegistryFixture(t)
	workspace := fixture.workspace(t, "project")

	alias := filepath.Join(fixture.root, "alias")
	require.NoError(t, os.Symlink(workspace, alias))

	first, err := fixture.registry.Open(t.Context(), workspace, "")
	require.NoError(t, err)

	viaAlias, err := fixture.registry.Open(t.Context(), alias, "")
	require.NoError(t, err)

	assert.Equal(t, first.Session.ID, viaAlias.Session.ID)
	assert.False(t, viaAlias.Created)
}

func TestRegistryOpenGivesDifferentWorkspacesDifferentSessions(t *testing.T) {
	fixture := newRegistryFixture(t)

	first, err := fixture.registry.Open(
		t.Context(),
		fixture.workspace(t, "a"),
		"",
	)
	require.NoError(t, err)

	second, err := fixture.registry.Open(
		t.Context(),
		fixture.workspace(t, "b"),
		"",
	)
	require.NoError(t, err)

	assert.NotEqual(t, first.Session.ID, second.Session.ID)
	assert.True(t, second.Created)
}

// A refused path must leave no trace, so a client cannot create rows for
// directories the deployment does not expose.
func TestRegistryOpenRefusesOutsideRootWithoutCreatingASession(t *testing.T) {
	fixture := newRegistryFixture(t)
	outside := canonicalTempDir(t)

	opened, err := fixture.registry.Open(t.Context(), outside, "")
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	assert.Nil(t, opened.Session)

	assertNoSessionsStored(t, fixture.store)
}

func TestRegistryOpenRefusesAMissingWorkspaceWithoutCreatingASession(
	t *testing.T,
) {
	fixture := newRegistryFixture(t)

	opened, err := fixture.registry.Open(
		t.Context(),
		filepath.Join(fixture.root, "absent"),
		"",
	)
	require.Error(t, err)
	assert.Nil(t, opened.Session)

	assertNoSessionsStored(t, fixture.store)
}

// A control surface holds no sessions until a client opens a workspace.
func TestRegistryStartsWithNoSessions(t *testing.T) {
	fixture := newRegistryFixture(t)

	assertNoSessionsStored(t, fixture.store)
}

func TestRegistryListSessionsReportsOpenedWorkspaces(t *testing.T) {
	fixture := newRegistryFixture(t)

	first, err := fixture.registry.Open(
		t.Context(),
		fixture.workspace(t, "a"),
		"",
	)
	require.NoError(t, err)

	second, err := fixture.registry.Open(
		t.Context(),
		fixture.workspace(t, "b"),
		"",
	)
	require.NoError(t, err)

	page, err := fixture.store.ListSessions(
		t.Context(),
		session.ListSessionsOptions{},
	)
	require.NoError(t, err)
	require.Len(t, page.Items, 2)

	stored := map[string]bool{}
	for _, item := range page.Items {
		stored[item.ID.String()] = true
	}

	assert.True(t, stored[first.Session.ID.String()])
	assert.True(t, stored[second.Session.ID.String()])
	assert.False(t, page.HasMore)
}

func assertNoSessionsStored(t *testing.T, store *session.Store) {
	t.Helper()

	page, err := store.ListSessions(
		context.Background(),
		session.ListSessionsOptions{},
	)
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}
