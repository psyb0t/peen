package session

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFreshWorkspaceSessionsPreserveCanonicalHistoryAcrossRestart(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	directory := filepath.Join(t.TempDir(), "state")
	workspace := t.TempDir()
	handle, err := db.Open(ctx, db.Config{Directory: directory})
	require.NoError(t, err)
	store := newTestStore(t, handle)
	options := OpenSessionOptions{RootAgent: "peen", ModelID: "provider/model"}
	type outcome struct {
		opened *OpenSessionResult
		err    error
	}
	results := make(chan outcome, 8)
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			opened, openErr := store.CreateWorkspaceSession(ctx, workspace, options)
			results <- outcome{opened, openErr}
		})
	}
	group.Wait()
	close(results)
	seen := make(map[uuid.UUID]bool)
	for item := range results {
		require.NoError(t, item.err)
		require.True(t, item.opened.Created)
		assert.False(t, seen[item.opened.Session.ID])
		seen[item.opened.Session.ID] = true
		assert.Equal(t, item.opened.Session.ID.String(), item.opened.Session.WorkspaceSlot)
		completeMessage(ctx, t, store, item.opened.Session.ID, "fresh history")
	}
	canonical, err := store.OpenWorkspace(ctx, workspace, options)
	require.NoError(t, err)
	assert.True(t, canonical.Created)
	assert.Empty(t, canonical.Session.WorkspaceSlot)
	assert.False(t, seen[canonical.Session.ID])
	require.NoError(t, handle.Close())
	handle, err = db.Open(ctx, db.Config{Directory: directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store = newTestStore(t, handle)
	resumed, err := store.OpenWorkspace(ctx, workspace, options)
	require.NoError(t, err)
	assert.False(t, resumed.Created)
	assert.Equal(t, canonical.Session.ID, resumed.Session.ID)
	for id := range seen {
		page, readErr := store.ListMessages(ctx, id, ListMessagesOptions{})
		require.NoError(t, readErr)
		assert.Equal(t, []string{"fresh history"}, messageContents(page.Items))
	}
	page, err := store.ListMessages(ctx, canonical.Session.ID, ListMessagesOptions{})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}
