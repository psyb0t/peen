package session

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testControlSpace = "control"
	testSandboxSpace = "docker:sandbox"
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

		group.Go(func() {
			opened, err := store.OpenWorkspace(t.Context(), workspace,
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

	require.Len(t, page.Items, 1)
	assert.Equal(t, winner, page.Items[0].AgentSpace)
}
