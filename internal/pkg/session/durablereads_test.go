package session

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreDurableReplayReadsEverySessionOwnedTable(t *testing.T) {
	ctx := context.Background()
	stateDirectory := filepath.Join(t.TempDir(), "state")
	handle, err := db.Open(ctx, db.Config{Directory: stateDirectory})
	require.NoError(t, err)
	store := newTestStore(t, handle)
	owner := newTestSession(ctx, t, store)
	other := newTestSession(ctx, t, store)

	const contextHash = "durable-replay-context"
	const promptHash = "durable-replay-prompt"
	require.NoError(t, store.SaveContextSnapshot(ctx, &models.ContextSnapshot{
		Hash:            contextHash,
		ManifestJSON:    `{"files":["AGENTS.md"]}`,
		ResolvedContent: "resolved context",
	}))
	require.NoError(t, store.SavePromptSnapshot(ctx, &models.PromptSnapshot{
		Hash:            promptHash,
		EffectivePrompt: "effective prompt",
	}))

	for index := range 3 {
		lease, acquireErr := store.AcquireTurn(ctx, owner.ID, StartTurnInput{
			RequestID: uuid.New(),
			Workspace: testWorkspace,
			Messages: []MessageInput{{
				Role:    models.MessageRoleUser,
				Content: "work item",
			}},
			Events: []EventInput{{
				RequestID:   uuid.New(),
				EventType:   "turn.started",
				PayloadJSON: fmt.Sprintf(`{"index":%d}`, index),
			}},
		})
		require.NoError(t, acquireErr)

		finalize := FinalizeTurnInput{State: models.TurnStateCompleted}
		if index == 0 {
			contextSnapshotHash := contextHash
			promptSnapshotHash := promptHash
			finalize.ContextSnapshotHash = &contextSnapshotHash
			finalize.PromptSnapshotHash = &promptSnapshotHash
		}
		require.NoError(t, store.FinalizeTurn(ctx, lease, finalize))
		store.ReleaseTurn(lease)
	}

	turns, err := store.ListTurns(ctx, owner.ID, ListTurnsOptions{Limit: 2})
	require.NoError(t, err)
	assert.Len(t, turns.Items, 2)
	assert.True(t, turns.HasMore)

	events, err := store.ListEvents(ctx, owner.ID, ListEventsOptions{Limit: 2})
	require.NoError(t, err)
	assert.Len(t, events.Items, 2)
	assert.True(t, events.HasMore)
	assert.Equal(t, int64(1), events.Items[0].Sequence)

	contextSnapshot, err := store.GetContextSnapshot(ctx, owner.ID, contextHash)
	require.NoError(t, err)
	assert.Equal(t, "resolved context", contextSnapshot.ResolvedContent)

	promptSnapshot, err := store.GetPromptSnapshot(ctx, owner.ID, promptHash)
	require.NoError(t, err)
	assert.Equal(t, "effective prompt", promptSnapshot.EffectivePrompt)

	_, err = store.GetContextSnapshot(ctx, other.ID, contextHash)
	require.ErrorIs(t, err, commerr.ErrNotFound)
	_, err = store.GetPromptSnapshot(ctx, other.ID, promptHash)
	require.ErrorIs(t, err, commerr.ErrNotFound)

	require.NoError(t, handle.Close())
	reopenedHandle, err := db.Open(ctx, db.Config{Directory: stateDirectory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopenedHandle.Close()) })
	reopened := newTestStore(t, reopenedHandle)

	replayed, err := reopened.ListEvents(
		ctx,
		owner.ID,
		ListEventsOptions{Limit: 5},
	)
	require.NoError(t, err)
	assert.Len(t, replayed.Items, 3)
	assert.False(t, replayed.HasMore)
}

func TestStoreSessionNoticesPersistUntilDelivery(t *testing.T) {
	ctx := context.Background()
	stateDirectory := filepath.Join(t.TempDir(), "state")
	handle, err := db.Open(ctx, db.Config{Directory: stateDirectory})
	require.NoError(t, err)
	store := newTestStore(t, handle)
	owner := newTestSession(ctx, t, store)

	for index := range 3 {
		_, createErr := store.CreateSessionNotice(ctx, owner.ID, CreateSessionNoticeInput{
			Type:     "app.changed",
			Source:   "test",
			Summary:  "changed",
			DataJSON: fmt.Sprintf(`{"index":%d}`, index),
			Delivery: models.NoticeDeliveryQueue,
		})
		require.NoError(t, createErr)
	}

	firstPage, err := store.ListSessionNotices(
		ctx,
		owner.ID,
		ListSessionNoticesOptions{Limit: 2},
	)
	require.NoError(t, err)
	assert.Len(t, firstPage.Items, 2)
	assert.True(t, firstPage.HasMore)
	assert.Equal(t, models.NoticeStatePending, firstPage.Items[0].State)

	drained, err := store.DrainSessionNotices(ctx, owner.ID)
	require.NoError(t, err)
	require.Len(t, drained, 3)
	assert.Equal(t, models.NoticeStateDelivered, drained[0].State)
	assert.NotNil(t, drained[0].DeliveredAt)

	empty, err := store.DrainSessionNotices(ctx, owner.ID)
	require.NoError(t, err)
	assert.Empty(t, empty)

	require.NoError(t, handle.Close())
	reopenedHandle, err := db.Open(ctx, db.Config{Directory: stateDirectory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopenedHandle.Close()) })
	reopened := newTestStore(t, reopenedHandle)

	history, err := reopened.ListSessionNotices(
		ctx,
		owner.ID,
		ListSessionNoticesOptions{Limit: 5},
	)
	require.NoError(t, err)
	require.Len(t, history.Items, 3)
	assert.Equal(t, models.NoticeStateDelivered, history.Items[2].State)
}
