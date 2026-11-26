package session

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConcurrentWorkspaceOpensPreserveHistoryAcrossRestart(t *testing.T) {
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
			opened, openErr := store.OpenWorkspace(ctx, workspace, options)
			results <- outcome{opened, openErr}
		})
	}
	group.Wait()
	close(results)
	current, err := store.OpenWorkspace(ctx, workspace, options)
	require.NoError(t, err)
	assert.False(t, current.Created)
	creations := 0
	for item := range results {
		require.NoError(t, item.err)
		assert.Equal(t, current.Session.ID, item.opened.Session.ID)
		if item.opened.Created {
			creations++
		}
	}
	assert.Equal(t, 1, creations)
	completeMessage(ctx, t, store, current.Session.ID, "workspace history")
	require.NoError(t, handle.Close())
	handle, err = db.Open(ctx, db.Config{Directory: directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store = newTestStore(t, handle)
	resumed, err := store.OpenWorkspace(ctx, workspace, options)
	require.NoError(t, err)
	assert.False(t, resumed.Created)
	assert.Equal(t, current.Session.ID, resumed.Session.ID)
	page, err := store.ListMessages(ctx, current.Session.ID, ListMessagesOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"workspace history"}, messageContents(page.Items))
}
