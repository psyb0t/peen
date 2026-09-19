package control_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testSandboxProfile = worker.ProfileDockerSandbox
	testSandboxImage   = "psyb0t/peen@sha256:" +
		"1111111111111111111111111111111111111111111111111111111111111111"
)

// reconfigureFixture is a registry over a real store with two allowed
// profiles.
//
// Workers is left nil on purpose. This file asserts the durable decision the
// control surface records; the worker replacement that follows a decision is
// asserted against a real socket and a real generation in the supervisor
// package.
//
// These tests do not call t.Parallel. db.Open installs the generated
// repositories as a package default, which is process-global state, so two
// fixtures opening a database at once race.
type reconfigureFixture struct {
	registry  *control.Registry
	store     *session.Store
	sessionID uuid.UUID
}

func newReconfigureFixture(t *testing.T) reconfigureFixture {
	t.Helper()

	handle, err := db.Open(t.Context(), db.Config{Directory: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	store, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)

	root := canonicalTempDir(t)

	policy, err := control.NewWorkspacePolicy([]string{root})
	require.NoError(t, err)

	profiles, err := worker.NewProfileSet(
		[]worker.Profile{
			{
				Name:     worker.ProfileNative,
				Kind:     worker.KindNative,
				Revision: 1,
			},
			{
				Name:     testSandboxProfile,
				Kind:     worker.KindDocker,
				Revision: 2,
				Image:    testSandboxImage,
			},
		},
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

	opened, err := registry.Open(t.Context(), root, worker.ProfileNative)
	require.NoError(t, err)

	return reconfigureFixture{
		registry:  registry,
		store:     store,
		sessionID: opened.Session.ID,
	}
}

// A profile change moves the session and records the caller's reason.
func TestRegistryReconfigureSessionRecordsTheDecision(t *testing.T) {
	fixture := newReconfigureFixture(t)

	decision, err := fixture.registry.ReconfigureSession(
		t.Context(),
		fixture.sessionID,
		api.ReconfigureSessionRequest{
			Profile: testSandboxProfile,
			Reason:  "isolating the workspace",
		},
	)
	require.NoError(t, err)

	assert.Equal(t, testSandboxProfile, decision.ToProfile)
	require.NotNil(t, decision.FromProfile)
	assert.Equal(t, worker.ProfileNative, *decision.FromProfile)
	assert.Equal(t, "isolating the workspace", decision.Reason)

	stored, err := fixture.store.Get(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	assert.Equal(t, testSandboxProfile, stored.ExecutionProfile)
}

// A profile the operator never defined is refused, and nothing changes.
func TestRegistryReconfigureSessionRefusesAnUndefinedProfile(t *testing.T) {
	fixture := newReconfigureFixture(t)

	_, err := fixture.registry.ReconfigureSession(
		t.Context(),
		fixture.sessionID,
		api.ReconfigureSessionRequest{
			Profile: worker.ProfileDockerHostLike,
			Reason:  "wants the host",
		},
	)

	require.ErrorIs(t, err, commerr.ErrPermissionDenied)

	stored, err := fixture.store.Get(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	assert.Equal(t, worker.ProfileNative, stored.ExecutionProfile)

	page, err := fixture.registry.ListProfileDecisions(
		t.Context(),
		fixture.sessionID,
		api.ListSessionProfileDecisionsParams{},
	)
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}

// The history reports every change newest first, with the reason given.
func TestRegistryListProfileDecisionsReportsTheHistory(t *testing.T) {
	fixture := newReconfigureFixture(t)

	changes := []struct {
		profile string
		reason  string
	}{
		{profile: testSandboxProfile, reason: "isolating the workspace"},
		{profile: worker.ProfileNative, reason: "back to native"},
	}

	for _, change := range changes {
		_, err := fixture.registry.ReconfigureSession(
			t.Context(),
			fixture.sessionID,
			api.ReconfigureSessionRequest{
				Profile: change.profile,
				Reason:  change.reason,
			},
		)
		require.NoError(t, err)
	}

	page, err := fixture.registry.ListProfileDecisions(
		t.Context(),
		fixture.sessionID,
		api.ListSessionProfileDecisionsParams{},
	)
	require.NoError(t, err)
	require.Len(t, page.Items, 2)

	assert.Equal(t, worker.ProfileNative, page.Items[0].ToProfile)
	assert.Equal(t, "back to native", page.Items[0].Reason)
	require.NotNil(t, page.Items[0].FromProfile)
	assert.Equal(t, testSandboxProfile, *page.Items[0].FromProfile)

	assert.Equal(t, testSandboxProfile, page.Items[1].ToProfile)
	assert.Equal(t, "isolating the workspace", page.Items[1].Reason)
}

// A session with a turn in flight is refused until that turn ends, because
// changing the profile under running work would attribute its tool calls to a
// profile that did not run them.
func TestRegistryReconfigureSessionRefusesABusySession(t *testing.T) {
	fixture := newReconfigureFixture(t)

	stored, err := fixture.store.Get(t.Context(), fixture.sessionID)
	require.NoError(t, err)

	lease, err := fixture.store.AcquireTurn(
		t.Context(),
		fixture.sessionID,
		session.StartTurnInput{
			RequestID: uuid.New(),
			Workspace: stored.Workspace,
		},
	)
	require.NoError(t, err)

	_, err = fixture.registry.ReconfigureSession(
		t.Context(),
		fixture.sessionID,
		api.ReconfigureSessionRequest{
			Profile: testSandboxProfile,
			Reason:  "isolating the workspace",
		},
	)
	require.ErrorIs(t, err, session.ErrSessionBusy)

	fixture.store.ReleaseTurn(lease)

	_, err = fixture.registry.ReconfigureSession(
		t.Context(),
		fixture.sessionID,
		api.ReconfigureSessionRequest{
			Profile: testSandboxProfile,
			Reason:  "isolating the workspace",
		},
	)
	require.NoError(t, err)
}
