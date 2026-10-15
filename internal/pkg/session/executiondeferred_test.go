package session

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func deferredTestLease(t *testing.T, fixture executionFixture) Lease {
	t.Helper()
	holder, err := fixture.store.GetExecutionTurnLease(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)

	return Lease{
		ID: holder.LeaseID, SessionID: fixture.sessionID,
		TurnID: fixture.root.RootTurnID,
	}
}

func TestDeferredMessagePersistsAndReusesFrozenInput(t *testing.T) {
	t.Parallel()
	fixture, _ := sealedCompletionFixture(t)
	lease := deferredTestLease(t, fixture)
	input := inboxTestInput()
	first, err := fixture.store.DeferExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.NoError(t, err)
	assert.Equal(t, models.InboxMessageStateDeferred, first.Message.State)
	repeated, err := fixture.store.DeferExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.NoError(t, err)
	assert.Equal(t, first.Message, repeated.Message)
	assert.Empty(t, repeated.Events)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store := newTestStore(t, handle)
	page, err := store.ListExecutionMessages(t.Context(), fixture.sessionID,
		fixture.root.ID, ListExecutionInboxOptions{
			State: models.InboxMessageStateDeferred,
		})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, first.Message, page.Items[0])
	assert.Contains(t, page.Items[0].PartsJSON, input.Parts[0].Content)
}

func TestDeferredMessageRequiresReservedCompletion(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	_, err := fixture.store.DeferExecutionMessage(t.Context(),
		fixture.root.ID, lease, inboxTestInput())
	require.ErrorIs(t, err, commerr.ErrConflict)
	page, err := fixture.store.ListExecutionMessages(t.Context(),
		fixture.sessionID, fixture.root.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}

func TestDeferredMessageCapacityAndChangedReplay(t *testing.T) {
	t.Parallel()
	fixture, _ := sealedCompletionFixture(t)
	lease := deferredTestLease(t, fixture)
	input := inboxTestInput()
	input.Capacity = 1 + len(input.Parts)
	_, err := fixture.store.DeferExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.NoError(t, err)
	input.Message = "changed replay"
	_, err = fixture.store.DeferExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	input.RequestID = uuid.New()
	_, err = fixture.store.DeferExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestDeferredMessageRejectsInvalidLeaseAndInput(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		change  func(*Lease, *AcceptExecutionMessageInput)
		wantErr error
	}{
		{name: "foreign session", change: func(lease *Lease,
			_ *AcceptExecutionMessageInput,
		) {
			lease.SessionID = uuid.New()
		}, wantErr: commerr.ErrInvalidState},
		{name: "stale lease", change: func(lease *Lease,
			_ *AcceptExecutionMessageInput,
		) {
			lease.ID = uuid.New()
		}, wantErr: commerr.ErrInvalidState},
		{name: "empty message", change: func(_ *Lease,
			input *AcceptExecutionMessageInput,
		) {
			input.Message = ""
		}, wantErr: commerr.ErrValidationFailed},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, _ := sealedCompletionFixture(t)
			lease := deferredTestLease(t, fixture)
			input := inboxTestInput()
			tc.change(&lease, &input)
			_, err := fixture.store.DeferExecutionMessage(t.Context(),
				fixture.root.ID, lease, input)
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestDeferredMessageCannotBecomeOldTurnInput(t *testing.T) {
	t.Parallel()
	fixture, _ := sealedCompletionFixture(t)
	lease := deferredTestLease(t, fixture)
	input := inboxTestInput()
	_, err := fixture.store.DeferExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.NoError(t, err)
	_, err = fixture.store.AcceptExecutionMessage(t.Context(), fixture.root.ID,
		lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	input.RequestID = uuid.New()
	_, err = fixture.store.AcceptExecutionMessage(t.Context(), fixture.root.ID,
		lease, input)
	require.NoError(t, err)
	_, err = fixture.store.DeferExecutionMessage(t.Context(), fixture.root.ID,
		lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
}
