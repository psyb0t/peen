package session

import (
	"encoding/json"
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failedLeaseRandomReader struct{}

func (failedLeaseRandomReader) Read(_ []byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func TestNewTurnLeaseReportsRandomSourceFailure(t *testing.T) {
	sessionID, turnID := uuid.New(), uuid.New()
	uuid.SetRand(failedLeaseRandomReader{})
	t.Cleanup(func() { uuid.SetRand(nil) })
	_, err := newTurnLease(sessionID, turnID)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestTurnLeaseIdentityFencesCallbacksForTheSameTurn(t *testing.T) {
	t.Parallel()
	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	opened := newTestSession(t.Context(), t, store)
	old := startAgentRunTestTurn(t.Context(), t, store, opened.ID)
	store.ReleaseTurn(old)
	current, err := newTurnLease(old.SessionID, old.TurnID)
	require.NoError(t, err)
	require.NoError(t, store.reserveLease(current))
	assert.NotEqual(t, old.ID, current.ID)
	message := []MessageInput{{
		Role:    models.MessageRoleAssistant,
		Content: "saved result",
	}}
	require.ErrorIs(t, store.AppendCheckpoint(t.Context(), old, message, nil),
		commerr.ErrInvalidState)
	require.ErrorIs(t, store.FinalizeTurn(t.Context(), old, FinalizeTurnInput{
		State: models.TurnStateCompleted,
	}), commerr.ErrInvalidState)
	require.ErrorIs(t, store.RecordTurnWorkerGeneration(t.Context(), old,
		uuid.New()), commerr.ErrInvalidState)
	store.ReleaseTurn(old)
	require.True(t, store.IsActive(opened.ID))
	require.NoError(t, store.AppendCheckpoint(t.Context(), current, message, nil))
	page, err := store.ListMessages(t.Context(), opened.ID, ListMessagesOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.Equal(t, "saved result", page.Items[1].Content)
}

func TestTurnLeaseIdentitySurvivesWireEncoding(t *testing.T) {
	t.Parallel()
	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	opened := newTestSession(t.Context(), t, store)
	lease := startAgentRunTestTurn(t.Context(), t, store, opened.ID)
	encoded, err := json.Marshal(lease)
	require.NoError(t, err)
	var restored Lease
	require.NoError(t, json.Unmarshal(encoded, &restored))
	assert.Equal(t, lease, restored)
	require.NotEqual(t, uuid.Nil, restored.ID)
	forged := restored
	forged.ID = uuid.Nil
	require.ErrorIs(t, store.AppendCheckpoint(t.Context(), forged, nil, nil),
		commerr.ErrInvalidState)
	require.NoError(t, store.AppendCheckpoint(t.Context(), restored, nil, nil))
}

func TestOldTurnLeaseCannotReplaceCancellationForTheSameTurn(t *testing.T) {
	t.Parallel()
	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	opened := newTestSession(t.Context(), t, store)
	old := startAgentRunTestTurn(t.Context(), t, store, opened.ID)
	store.ReleaseTurn(old)
	current, err := newTurnLease(old.SessionID, old.TurnID)
	require.NoError(t, err)
	require.NoError(t, store.reserveLease(current))
	oldCancelled, currentCancelled := false, false
	store.RegisterCancellation(current, func() { currentCancelled = true })
	store.RegisterCancellation(old, func() { oldCancelled = true })
	cancelled, err := store.Cancel(t.Context(), opened.ID)
	require.NoError(t, err)
	assert.True(t, cancelled)
	assert.True(t, currentCancelled)
	assert.False(t, oldCancelled)
}
