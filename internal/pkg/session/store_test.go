package session

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testWorkspace = "/workspace/project"

func TestStoreGetMapsMissingSessionToCommonError(t *testing.T) {
	t.Parallel()

	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	_, err := store.Get(context.Background(), uuid.New())

	require.ErrorIs(t, err, commerr.ErrNotFound)
}

func TestStoreCreateOrResumeCreatesRequestedSessionIDAtomically(t *testing.T) {
	ctx := context.Background()
	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	requestedSessionID := uuid.New()

	type result struct {
		session *models.Session
		created bool
		err     error
	}

	start := make(chan struct{})
	results := make(chan result, 2)
	var waitGroup sync.WaitGroup
	for range 2 {
		waitGroup.Go(func() {
			<-start
			opened, err := store.CreateOrResume(
				ctx,
				&requestedSessionID,
				OpenSessionOptions{
					RootAgent: "peen",
					ModelID:   "provider/model",
				},
			)
			if err != nil {
				results <- result{err: err}

				return
			}

			results <- result{session: opened.Session, created: opened.Created}
		})
	}

	close(start)
	waitGroup.Wait()
	close(results)

	createdCount := 0
	for opened := range results {
		require.NoError(t, opened.err)
		require.NotNil(t, opened.session)
		assert.Equal(t, requestedSessionID, opened.session.ID)
		if opened.created {
			createdCount++
		}
	}
	assert.Equal(t, 1, createdCount)
}

func TestStoreRestartPaginationIsolationAndCompaction(t *testing.T) {
	ctx := context.Background()
	stateDirectory := filepath.Join(t.TempDir(), "state")
	handle, err := db.Open(ctx, db.Config{Directory: stateDirectory})
	require.NoError(t, err)

	store := newTestStore(t, handle)
	sessionA := newTestSession(ctx, t, store)
	sessionB := newTestSession(ctx, t, store)

	messageIDs := make([]uuid.UUID, 0, 3)
	for _, content := range []string{"one", "two", "three"} {
		messageIDs = append(messageIDs, completeMessage(ctx, t, store, sessionA.ID, content))
	}

	completeMessage(ctx, t, store, sessionB.ID, "other session")

	pageCases := []struct {
		name        string
		options     ListMessagesOptions
		want        []string
		wantHasMore bool
	}{
		{
			name:        "ascending first page",
			options:     ListMessagesOptions{Limit: 2, Order: PageOrderAscending},
			want:        []string{"one", "two"},
			wantHasMore: true,
		},
		{
			name:        "ascending tail",
			options:     ListMessagesOptions{Limit: 2, Offset: 2, Order: PageOrderAscending},
			want:        []string{"three"},
			wantHasMore: false,
		},
		{
			name:        "descending first page",
			options:     ListMessagesOptions{Limit: 2, Order: PageOrderDescending},
			want:        []string{"three", "two"},
			wantHasMore: true,
		},
	}
	for _, tc := range pageCases {
		t.Run(tc.name, func(t *testing.T) {
			page, pageErr := store.ListMessages(ctx, sessionA.ID, tc.options)
			require.NoError(t, pageErr)
			assert.Equal(t, tc.want, messageContents(page.Items))
			assert.Equal(t, tc.wantHasMore, page.HasMore)
		})
	}

	isolated, err := store.ListMessages(
		ctx,
		sessionB.ID,
		ListMessagesOptions{Limit: 10, Order: PageOrderAscending},
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"other session"}, messageContents(isolated.Items))

	firstCompaction, err := store.CreateCompaction(ctx, sessionA.ID, CompactionInput{
		FromMessageID:      messageIDs[0],
		ToMessageID:        messageIDs[0],
		FromSequence:       1,
		ToSequence:         1,
		Summary:            "first summary",
		SourceMessageCount: 1,
		ModelID:            "provider/model",
		PromptHash:         "prompt-one",
	})
	require.NoError(t, err)
	secondCompaction, err := store.CreateCompaction(ctx, sessionA.ID, CompactionInput{
		FromMessageID:          messageIDs[0],
		ToMessageID:            messageIDs[1],
		FromSequence:           1,
		ToSequence:             2,
		Summary:                "second summary",
		SourceMessageCount:     2,
		ModelID:                "provider/model",
		PromptHash:             "prompt-two",
		SupersedesCompactionID: &firstCompaction.ID,
	})
	require.NoError(t, err)

	history, err := store.CompletedHistory(ctx, sessionA.ID)
	require.NoError(t, err)
	require.NotNil(t, history.Compaction)
	assert.Equal(t, secondCompaction.ID, history.Compaction.ID)
	assert.Equal(t, []string{"three"}, messageContents(history.Messages))

	require.NoError(t, handle.Close())

	reopenedHandle, err := db.Open(ctx, db.Config{Directory: stateDirectory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopenedHandle.Close()) })
	reopenedStore := newTestStore(t, reopenedHandle)

	reopenedSession, err := reopenedStore.Get(ctx, sessionA.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(3), reopenedSession.MessageCount)
	assert.Equal(t, int64(3), reopenedSession.LastCompletedSequence)

	reopenedHistory, err := reopenedStore.CompletedHistory(ctx, sessionA.ID)
	require.NoError(t, err)
	assert.Equal(t, secondCompaction.ID, reopenedHistory.Compaction.ID)
	assert.Equal(t, []string{"three"}, messageContents(reopenedHistory.Messages))
}

func TestStorePaginationWalksEveryPage(t *testing.T) {
	ctx := context.Background()
	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	session := newTestSession(ctx, t, store)

	want := make([]string, 0, 13)

	for index := range 13 {
		content := fmt.Sprintf("message-%02d", index)
		want = append(want, content)
		completeMessage(ctx, t, store, session.ID, content)
	}

	var got []string

	for offset := 0; ; offset += 5 {
		page, err := store.ListMessages(ctx, session.ID, ListMessagesOptions{
			Limit:  5,
			Offset: offset,
			Order:  PageOrderAscending,
		})
		require.NoError(t, err)

		got = append(got, messageContents(page.Items)...)
		if !page.HasMore {
			break
		}
	}

	assert.Equal(t, want, got)

	for _, offset := range []int{len(want), len(want) + 100} {
		page, err := store.ListMessages(ctx, session.ID, ListMessagesOptions{
			Limit:  5,
			Offset: offset,
			Order:  PageOrderAscending,
		})
		require.NoError(t, err)
		assert.Empty(t, page.Items)
		assert.False(t, page.HasMore)
	}
}

func TestStoreSnapshotsRecoveryAndCancellationIdentity(t *testing.T) {
	ctx := context.Background()
	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	session := newTestSession(ctx, t, store)

	contextHash := "context-hash"
	promptHash := "prompt-hash"

	require.NoError(t, store.SaveContextSnapshot(ctx, &models.ContextSnapshot{
		Hash:            contextHash,
		ManifestJSON:    `{}`,
		ResolvedContent: "resolved",
	}))
	require.NoError(t, store.SaveContextSnapshot(ctx, &models.ContextSnapshot{
		Hash:            contextHash,
		ManifestJSON:    `{}`,
		ResolvedContent: "resolved",
	}))
	require.NoError(t, store.SavePromptSnapshot(ctx, &models.PromptSnapshot{
		Hash:            promptHash,
		EffectivePrompt: "prompt",
	}))
	require.NoError(t, store.SavePromptSnapshot(ctx, &models.PromptSnapshot{
		Hash:            promptHash,
		EffectivePrompt: "prompt",
	}))

	lease, err := store.AcquireTurn(ctx, session.ID, StartTurnInput{
		RequestID: uuid.New(),
		Workspace: testWorkspace,
	})
	require.NoError(t, err)

	callbackCalls := 0
	cancelled, err := store.Cancel(ctx, session.ID)
	require.NoError(t, err)
	assert.True(t, cancelled)
	store.RegisterCancellation(lease, func() { callbackCalls++ })
	assert.Equal(t, 1, callbackCalls)

	cancelled, err = store.Cancel(ctx, session.ID)
	require.NoError(t, err)
	assert.True(t, cancelled)
	assert.Equal(t, 1, callbackCalls)

	require.NoError(t, store.FinalizeTurn(ctx, lease, FinalizeTurnInput{
		State:               models.TurnStateCancelled,
		ContextSnapshotHash: &contextHash,
		PromptSnapshotHash:  &promptHash,
		Messages: []MessageInput{{
			Role:    models.MessageRoleUser,
			Content: "cancelled request",
		}},
		Events: []EventInput{{
			RequestID:   uuid.New(),
			EventType:   "turn.cancelled",
			PayloadJSON: `{}`,
		}},
	}))

	leaseTwo, err := store.AcquireTurn(ctx, session.ID, StartTurnInput{
		RequestID: uuid.New(),
		Workspace: testWorkspace,
	})
	require.NoError(t, err)
	store.ReleaseTurn(lease)

	staleCallbackCalls := 0

	store.RegisterCancellation(lease, func() { staleCallbackCalls++ })
	assert.Zero(t, staleCallbackCalls)

	_, err = store.AcquireTurn(ctx, session.ID, StartTurnInput{Workspace: testWorkspace})
	require.ErrorIs(t, err, ErrSessionBusy)

	leaseTwoCallbackCalls := 0

	store.RegisterCancellation(leaseTwo, func() { leaseTwoCallbackCalls++ })
	cancelled, err = store.Cancel(ctx, session.ID)
	require.NoError(t, err)
	assert.True(t, cancelled)
	assert.Equal(t, 1, leaseTwoCallbackCalls)
	require.NoError(t, store.FinalizeTurn(ctx, leaseTwo, FinalizeTurnInput{
		State: models.TurnStateCancelled,
		Messages: []MessageInput{{
			Role:    models.MessageRoleAssistant,
			Content: "cancelled",
		}},
	}))

	runningLease, err := store.AcquireTurn(ctx, session.ID, StartTurnInput{
		RequestID: uuid.New(),
		Workspace: testWorkspace,
		Messages: []MessageInput{{
			Role:    models.MessageRoleUser,
			Content: "interrupted",
		}},
	})
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, runningLease.TurnID)

	recovered, err := store.RecoverInterrupted(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, recovered)

	recoveredAgain, err := store.RecoverInterrupted(ctx)
	require.NoError(t, err)
	assert.Zero(t, recoveredAgain)

	postRecoveryLease, err := store.AcquireTurn(ctx, session.ID, StartTurnInput{
		RequestID: uuid.New(),
		Workspace: testWorkspace,
	})
	require.NoError(t, err)
	require.NoError(t, store.FinalizeTurn(ctx, postRecoveryLease, FinalizeTurnInput{
		State: models.TurnStateCompleted,
	}))

	messages, err := store.ListMessages(
		ctx,
		session.ID,
		ListMessagesOptions{Limit: 10, Order: PageOrderAscending},
	)
	require.NoError(t, err)
	require.Len(t, messages.Items, 3)
	assert.Equal(t, testWorkspace, messages.Items[0].Workspace)
	assert.True(t, messages.Items[2].Incomplete)
	assert.Equal(t, defaultToolCallsJSON, messages.Items[0].ToolCallsJSON)

	turn := store.query.Turn
	persistedTurn, err := turn.WithContext(ctx).Where(turn.ID.Eq(lease.TurnID)).First()
	require.NoError(t, err)
	require.NotNil(t, persistedTurn.ContextSnapshotHash)
	require.NotNil(t, persistedTurn.PromptSnapshotHash)
	assert.Equal(t, contextHash, *persistedTurn.ContextSnapshotHash)
	assert.Equal(t, promptHash, *persistedTurn.PromptSnapshotHash)

	event := store.query.Event
	persistedEvent, err := event.WithContext(ctx).
		Where(event.TurnID.Eq(lease.TurnID)).
		First()
	require.NoError(t, err)
	assert.Equal(t, "turn.cancelled", persistedEvent.EventType)
	assert.JSONEq(t, `{}`, persistedEvent.PayloadJSON)
}

func TestStoreRecoverInterruptedExcludedFromCompletedHistory(t *testing.T) {
	ctx := context.Background()
	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	session := newTestSession(ctx, t, store)

	completeMessage(ctx, t, store, session.ID, "completed before interruption")

	interruptedLease, err := store.AcquireTurn(ctx, session.ID, StartTurnInput{
		RequestID: uuid.New(),
		Workspace: testWorkspace,
		Messages: []MessageInput{{
			Role:    models.MessageRoleUser,
			Content: "interrupted turn message",
		}},
	})
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, interruptedLease.TurnID)

	recovered, err := store.RecoverInterrupted(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, recovered)

	// Failed and cancelled turns retain their events and an incomplete
	// checkpoint but are excluded from later model context: only the
	// previously completed turn's message survives into CompletedHistory.
	history, err := store.CompletedHistory(ctx, session.ID)
	require.NoError(t, err)
	assert.Equal(t,
		[]string{"completed before interruption"},
		messageContents(history.Messages),
	)

	allMessages, err := store.ListMessages(
		ctx,
		session.ID,
		ListMessagesOptions{Limit: 10, Order: PageOrderAscending},
	)
	require.NoError(t, err)
	require.Len(t, allMessages.Items, 2)
	assert.False(t, allMessages.Items[0].Incomplete)
	assert.Equal(t, "completed before interruption", allMessages.Items[0].Content)
	assert.True(t, allMessages.Items[1].Incomplete)
	assert.Equal(t, "interrupted turn message", allMessages.Items[1].Content)
}

func TestStoreRejectsConcurrentLease(t *testing.T) {
	ctx := context.Background()
	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	session := newTestSession(ctx, t, store)

	type result struct {
		lease Lease
		err   error
	}

	start := make(chan struct{})
	results := make(chan result, 2)

	var waitGroup sync.WaitGroup
	for range 2 {
		waitGroup.Go(func() {
			<-start

			lease, err := store.AcquireTurn(ctx, session.ID, StartTurnInput{
				RequestID: uuid.New(),
				Workspace: testWorkspace,
			})
			results <- result{lease: lease, err: err}
		})
	}

	close(start)
	waitGroup.Wait()
	close(results)

	var (
		acquired  Lease
		busyCount int
	)

	for result := range results {
		if result.err == nil {
			acquired = result.lease

			continue
		}

		if errors.Is(result.err, ErrSessionBusy) {
			busyCount++
		}
	}

	require.NotEqual(t, uuid.Nil, acquired.TurnID)
	assert.Equal(t, 1, busyCount)
	require.NoError(t, store.FinalizeTurn(ctx, acquired, FinalizeTurnInput{
		State: models.TurnStateCompleted,
	}))
}

func TestStoreRejectsInvalidTranscriptJSON(t *testing.T) {
	ctx := context.Background()
	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	session := newTestSession(ctx, t, store)

	testCases := []struct {
		name     string
		messages []MessageInput
		events   []EventInput
	}{
		{
			name: "message tool calls",
			messages: []MessageInput{{
				Role:          models.MessageRoleUser,
				ToolCallsJSON: "not JSON",
			}},
		},
		{
			name: "event payload",
			events: []EventInput{{
				RequestID:   uuid.New(),
				EventType:   "tool.started",
				PayloadJSON: "not JSON",
			}},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			before, err := store.ListMessages(
				ctx,
				session.ID,
				ListMessagesOptions{Limit: 100, Order: PageOrderAscending},
			)
			require.NoError(t, err)
			lease, err := store.AcquireTurn(ctx, session.ID, StartTurnInput{
				RequestID: uuid.New(),
				Workspace: testWorkspace,
			})
			require.NoError(t, err)
			err = store.AppendCheckpoint(ctx, lease, tc.messages, tc.events)
			require.Error(t, err)
			require.NoError(t, store.FinalizeTurn(ctx, lease, FinalizeTurnInput{
				State: models.TurnStateFailed,
			}))
			after, err := store.ListMessages(
				ctx,
				session.ID,
				ListMessagesOptions{Limit: 100, Order: PageOrderAscending},
			)
			require.NoError(t, err)
			assert.Equal(t, messageContents(before.Items), messageContents(after.Items))
		})
	}
}

func openTestStore(t *testing.T) (*Store, *db.Handle) {
	t.Helper()

	handle, err := db.Open(context.Background(), db.Config{Directory: t.TempDir()})
	require.NoError(t, err)
	store := newTestStore(t, handle)

	return store, handle
}

func newTestStore(t *testing.T, handle *db.Handle) *Store {
	t.Helper()

	store, err := NewStore(handle, Options{})
	require.NoError(t, err)

	return store
}

func newTestSession(ctx context.Context, t *testing.T, store *Store) *models.Session {
	t.Helper()

	result, err := store.CreateOrResume(ctx, nil, OpenSessionOptions{
		RootAgent: "peen",
		ModelID:   "provider/model",
	})
	require.NoError(t, err)
	require.True(t, result.Created)

	return result.Session
}

func completeMessage(
	ctx context.Context,
	t *testing.T,
	store *Store,
	sessionID uuid.UUID,
	content string,
) uuid.UUID {
	t.Helper()

	messageID := uuid.New()
	lease, err := store.AcquireTurn(ctx, sessionID, StartTurnInput{
		RequestID: uuid.New(),
		Workspace: testWorkspace,
	})
	require.NoError(t, err)
	require.NoError(t, store.FinalizeTurn(ctx, lease, FinalizeTurnInput{
		State: models.TurnStateCompleted,
		Messages: []MessageInput{{
			ID:      messageID,
			Role:    models.MessageRoleUser,
			Content: content,
		}},
	}))

	return messageID
}

func messageContents(messages []*models.Message) []string {
	contents := make([]string, 0, len(messages))
	for _, message := range messages {
		contents = append(contents, message.Content)
	}

	return contents
}
