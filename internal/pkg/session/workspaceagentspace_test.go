package session

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testControlSpace = "control"
	testSandboxSpace = "docker:sandbox"
	testMoveReason   = "isolate the workspace"
)

func newAgentSpaceStore(t *testing.T) *Store {
	t.Helper()

	handle, err := db.Open(t.Context(), db.Config{Directory: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	return newTestStore(t, handle)
}

func canonicalWorkspaceDir(t *testing.T) string {
	t.Helper()

	canonical, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	return canonical
}

func agentSpaceOptions(requested string) OpenSessionOptions {
	return OpenSessionOptions{
		RootAgent: "peen", ModelID: "provider/model",
		AgentSpace: requested, DefaultAgentSpace: testControlSpace,
	}
}

func requireAgentSpaceConflict(t *testing.T, err error, current string) {
	t.Helper()

	require.ErrorIs(t, err, commerr.ErrConflict)

	var conflict *AgentSpaceConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, current, conflict.Current)
	assert.Contains(t, conflict.Error(), current)
}

func TestWorkspaceFirstOpenerChoosesTheAgentSpace(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		requested string
		expected  string
	}{
		{name: "default", expected: testControlSpace},
		{name: "named", requested: testSandboxSpace, expected: testSandboxSpace},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := newAgentSpaceStore(t)
			workspace := canonicalWorkspaceDir(t)

			opened, err := store.OpenWorkspace(t.Context(), workspace,
				agentSpaceOptions(tc.requested))
			require.NoError(t, err)
			assert.Equal(t, tc.expected, opened.Session.AgentSpace)

			space, found, err := store.WorkspaceAgentSpace(t.Context(),
				workspace)
			require.NoError(t, err)
			assert.True(t, found)
			assert.Equal(t, tc.expected, space)
		})
	}
}

func TestWorkspaceWithoutSessionsHasNoAgentSpace(t *testing.T) {
	t.Parallel()

	store := newAgentSpaceStore(t)

	space, found, err := store.WorkspaceAgentSpace(t.Context(),
		canonicalWorkspaceDir(t))
	require.NoError(t, err)
	assert.False(t, found)
	assert.Empty(t, space)
}

func TestWorkspaceSessionsShareOneAgentSpace(t *testing.T) {
	t.Parallel()

	store := newAgentSpaceStore(t)
	workspace := canonicalWorkspaceDir(t)

	canonical, err := store.OpenWorkspace(t.Context(), workspace,
		agentSpaceOptions(testSandboxSpace))
	require.NoError(t, err)

	reopened, err := store.OpenWorkspace(t.Context(), workspace,
		agentSpaceOptions(""))
	require.NoError(t, err)
	assert.False(t, reopened.Created)
	assert.Equal(t, canonical.Session.ID, reopened.Session.ID)

	same, err := store.OpenWorkspace(t.Context(), workspace,
		agentSpaceOptions(testSandboxSpace))
	require.NoError(t, err)
	assert.Equal(t, canonical.Session.ID, same.Session.ID)

	_, err = store.OpenWorkspace(t.Context(), workspace,
		agentSpaceOptions(testControlSpace))
	requireAgentSpaceConflict(t, err, testSandboxSpace)

	fresh, err := store.CreateWorkspaceSession(t.Context(), workspace,
		agentSpaceOptions(""))
	require.NoError(t, err)
	assert.Equal(t, testSandboxSpace, fresh.Session.AgentSpace)

	named, err := store.CreateWorkspaceSession(t.Context(), workspace,
		agentSpaceOptions(testSandboxSpace))
	require.NoError(t, err)
	assert.Equal(t, testSandboxSpace, named.Session.AgentSpace)

	_, err = store.CreateWorkspaceSession(t.Context(), workspace,
		agentSpaceOptions(testControlSpace))
	requireAgentSpaceConflict(t, err, testSandboxSpace)

	page, err := store.ListSessions(t.Context(), ListSessionsOptions{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 3)
}

// A fresh session made before the canonical one decides the workspace's agent
// space, and the canonical session opened later follows it.
func TestFreshSessionDecidesTheAgentSpaceBeforeTheCanonicalOne(t *testing.T) {
	t.Parallel()

	store := newAgentSpaceStore(t)
	workspace := canonicalWorkspaceDir(t)

	_, err := store.CreateWorkspaceSession(t.Context(), workspace,
		agentSpaceOptions(testSandboxSpace))
	require.NoError(t, err)

	_, err = store.OpenWorkspace(t.Context(), workspace,
		agentSpaceOptions(testControlSpace))
	requireAgentSpaceConflict(t, err, testSandboxSpace)

	canonical, err := store.OpenWorkspace(t.Context(), workspace,
		agentSpaceOptions(""))
	require.NoError(t, err)
	assert.True(t, canonical.Created)
	assert.Equal(t, testSandboxSpace, canonical.Session.AgentSpace)
}

// Concurrent first opens naming different agent spaces leave the workspace in
// exactly one of them: every request for the winner succeeds and every
// request for the other is refused.
func TestConcurrentWorkspaceCreatesAgreeOnOneAgentSpace(t *testing.T) {
	t.Parallel()

	store := newAgentSpaceStore(t)
	workspace := canonicalWorkspaceDir(t)

	type outcome struct {
		requested string
		space     string
		err       error
	}

	results := make(chan outcome, 16)

	var group sync.WaitGroup

	for index := range 16 {
		requested := testControlSpace
		if index%2 == 1 {
			requested = testSandboxSpace
		}

		open := store.CreateWorkspaceSession
		if index%4 < 2 {
			open = store.OpenWorkspace
		}

		group.Go(func() {
			opened, err := open(t.Context(), workspace,
				agentSpaceOptions(requested))
			if err != nil {
				results <- outcome{requested: requested, err: err}

				return
			}

			results <- outcome{
				requested: requested, space: opened.Session.AgentSpace,
			}
		})
	}

	group.Wait()
	close(results)

	winner, found, err := store.WorkspaceAgentSpace(t.Context(), workspace)
	require.NoError(t, err)
	require.True(t, found)

	for result := range results {
		if result.requested == winner {
			require.NoError(t, result.err)
			assert.Equal(t, winner, result.space)

			continue
		}

		requireAgentSpaceConflict(t, result.err, winner)
	}

	page, err := store.ListSessions(t.Context(), ListSessionsOptions{})
	require.NoError(t, err)

	for _, stored := range page.Items {
		assert.Equal(t, winner, stored.AgentSpace)
	}
}

func TestReconfigureMovesEveryWorkspaceSessionTogether(t *testing.T) {
	t.Parallel()

	store := newAgentSpaceStore(t)
	workspace := canonicalWorkspaceDir(t)

	canonical, err := store.OpenWorkspace(t.Context(), workspace,
		agentSpaceOptions(""))
	require.NoError(t, err)
	fresh, err := store.CreateWorkspaceSession(t.Context(), workspace,
		agentSpaceOptions(""))
	require.NoError(t, err)
	unrelated, err := store.OpenWorkspace(t.Context(),
		canonicalWorkspaceDir(t), agentSpaceOptions(""))
	require.NoError(t, err)

	decisions, err := store.ReconfigureAgentSpace(t.Context(),
		fresh.Session.ID, testSandboxSpace, testMoveReason)
	require.NoError(t, err)
	require.Len(t, decisions, 2)
	assert.Equal(t, fresh.Session.ID, decisions[0].SessionID)
	assert.Equal(t, canonical.Session.ID, decisions[1].SessionID)

	for _, id := range []uuid.UUID{canonical.Session.ID, fresh.Session.ID} {
		stored, getErr := store.Get(t.Context(), id)
		require.NoError(t, getErr)
		assert.Equal(t, testSandboxSpace, stored.AgentSpace)

		history, listErr := store.ListSessionAgentSpaceDecisions(t.Context(),
			id, ListAgentSpaceDecisionsOptions{})
		require.NoError(t, listErr)
		require.Len(t, history.Items, 1)
		assert.Equal(t, testControlSpace, history.Items[0].FromAgentSpace)
		assert.Equal(t, testSandboxSpace, history.Items[0].ToAgentSpace)
		assert.Equal(t, testMoveReason, history.Items[0].Reason)
	}

	stored, err := store.Get(t.Context(), unrelated.Session.ID)
	require.NoError(t, err)
	assert.Equal(t, testControlSpace, stored.AgentSpace)

	reopened, err := store.OpenWorkspace(t.Context(), workspace,
		agentSpaceOptions(testSandboxSpace))
	require.NoError(t, err)
	assert.Equal(t, canonical.Session.ID, reopened.Session.ID)
}

func TestReconfigureRefusesWhileAnyWorkspaceSessionRunsATurn(t *testing.T) {
	t.Parallel()

	store := newAgentSpaceStore(t)
	workspace := canonicalWorkspaceDir(t)

	canonical, err := store.OpenWorkspace(t.Context(), workspace,
		agentSpaceOptions(""))
	require.NoError(t, err)
	fresh, err := store.CreateWorkspaceSession(t.Context(), workspace,
		agentSpaceOptions(""))
	require.NoError(t, err)

	lease, err := store.AcquireTurn(t.Context(), fresh.Session.ID,
		StartTurnInput{RequestID: uuid.New(), Workspace: workspace})
	require.NoError(t, err)

	_, err = store.ReconfigureAgentSpace(t.Context(), canonical.Session.ID,
		testSandboxSpace, testMoveReason)
	require.ErrorIs(t, err, ErrSessionBusy)

	for _, id := range []uuid.UUID{canonical.Session.ID, fresh.Session.ID} {
		stored, getErr := store.Get(t.Context(), id)
		require.NoError(t, getErr)
		assert.Equal(t, testControlSpace, stored.AgentSpace)

		history, listErr := store.ListSessionAgentSpaceDecisions(t.Context(),
			id, ListAgentSpaceDecisionsOptions{})
		require.NoError(t, listErr)
		assert.Empty(t, history.Items)
	}

	store.ReleaseTurn(lease)

	_, err = store.ReconfigureAgentSpace(t.Context(), canonical.Session.ID,
		testSandboxSpace, testMoveReason)
	require.NoError(t, err)
}
