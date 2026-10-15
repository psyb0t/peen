package session

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	modernsqlite "modernc.org/sqlite"
	sqlitecodes "modernc.org/sqlite/lib"
)

func TestTurnOpeningNoticeRejectsForeignOwnership(t *testing.T) {
	t.Parallel()
	store, ownerID, noticeID := openingNoticeFixture(t)
	lease, err := store.AcquireTurn(t.Context(), ownerID,
		StartTurnInput{RequestID: uuid.New(), Workspace: testWorkspace})
	require.NoError(t, err)
	t.Cleanup(func() { store.ReleaseTurn(lease) })
	foreign := newTestSession(t.Context(), t, store)
	otherLease, err := store.AcquireTurn(t.Context(), foreign.ID,
		StartTurnInput{RequestID: uuid.New(), Workspace: testWorkspace})
	require.NoError(t, err)
	t.Cleanup(func() { store.ReleaseTurn(otherLease) })
	otherNotice, err := store.CreateSessionNotice(t.Context(), foreign.ID,
		CreateSessionNoticeInput{
			Type: "app.ready", Source: "example", Summary: "ready",
			DataJSON: `{}`, Delivery: models.NoticeDeliveryQueue,
		})
	require.NoError(t, err)
	testCases := []struct {
		name     string
		turnID   uuid.UUID
		noticeID uuid.UUID
	}{
		{name: "foreign turn", turnID: otherLease.TurnID, noticeID: noticeID},
		{name: "foreign notice", turnID: lease.TurnID, noticeID: otherNotice.ID},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := store.query.TurnOpeningNotice.WithContext(t.Context()).
				Create(&models.TurnOpeningNotice{
					SessionID: ownerID, TurnID: tc.turnID,
					NoticeID: tc.noticeID, SourceSequence: 1,
				})
			require.ErrorIs(t, err, gorm.ErrForeignKeyViolated)
			count, err := store.query.TurnOpeningNotice.WithContext(t.Context()).
				Count()
			require.NoError(t, err)
			assert.Zero(t, count)
		})
	}
}

func TestTurnOpeningNoticeRejectsZeroSequence(t *testing.T) {
	t.Parallel()
	store, ownerID, noticeID := openingNoticeFixture(t)
	lease, err := store.AcquireTurn(t.Context(), ownerID,
		StartTurnInput{RequestID: uuid.New(), Workspace: testWorkspace})
	require.NoError(t, err)
	t.Cleanup(func() { store.ReleaseTurn(lease) })
	err = store.query.TurnOpeningNotice.WithContext(t.Context()).
		Create(&models.TurnOpeningNotice{
			SessionID: ownerID, TurnID: lease.TurnID, NoticeID: noticeID,
		})
	var native *modernsqlite.Error
	require.ErrorAs(t, err, &native)
	assert.Equal(t, sqlitecodes.SQLITE_CONSTRAINT_CHECK, native.Code())
}

func openingNoticeFixture(t *testing.T) (*Store, uuid.UUID, uuid.UUID) {
	t.Helper()
	fixture := newExecutionFixture(t)
	owner := newTestSession(t.Context(), t, fixture.store)
	notice, err := fixture.store.CreateSessionNotice(t.Context(), owner.ID,
		CreateSessionNoticeInput{
			Type: "app.ready", Source: "example", Summary: "ready",
			DataJSON: `{}`, Delivery: models.NoticeDeliveryQueue,
		})
	require.NoError(t, err)

	return fixture.store, owner.ID, notice.ID
}

func TestTurnNoticeAdmissionRefusalRetainsRowsAndReleasesLease(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		ids       func(uuid.UUID, uuid.UUID) []uuid.UUID
		delivered bool
		wantErr   error
	}{
		{
			name: "missing", wantErr: commerr.ErrNotFound,
			ids: func(_, _ uuid.UUID) []uuid.UUID { return []uuid.UUID{uuid.New()} },
		},
		{
			name: "zero", wantErr: commerr.ErrRequiredFieldNotSet,
			ids: func(_, _ uuid.UUID) []uuid.UUID { return []uuid.UUID{uuid.Nil} },
		},
		{
			name: "foreign", wantErr: commerr.ErrNotFound,
			ids: func(_, foreign uuid.UUID) []uuid.UUID {
				return []uuid.UUID{foreign}
			},
		},
		{
			name: "duplicate", wantErr: commerr.ErrValidationFailed,
			ids: func(own, _ uuid.UUID) []uuid.UUID { return []uuid.UUID{own, own} },
		},
		{
			name: "delivered", delivered: true, wantErr: commerr.ErrConflict,
			ids: func(own, _ uuid.UUID) []uuid.UUID { return []uuid.UUID{own} },
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, ownerID, noticeID := openingNoticeFixture(t)
			foreign := newTestSession(t.Context(), t, store)
			other, err := store.CreateSessionNotice(t.Context(), foreign.ID,
				CreateSessionNoticeInput{
					Type: "app.ready", Source: "example", Summary: "ready",
					DataJSON: `{}`, Delivery: models.NoticeDeliveryQueue,
				})
			require.NoError(t, err)
			if tc.delivered {
				_, err := store.DrainSessionNotices(t.Context(), ownerID)
				require.NoError(t, err)
			}
			before, err := store.ListSessionNotices(t.Context(), ownerID,
				ListSessionNoticesOptions{})
			require.NoError(t, err)
			_, err = store.AcquireTurn(t.Context(), ownerID, StartTurnInput{
				RequestID: uuid.New(), Workspace: testWorkspace,
				NoticeIDs: tc.ids(noticeID, other.ID),
				Messages:  []MessageInput{{Role: models.MessageRoleUser, Content: "ready"}},
			})
			require.ErrorIs(t, err, tc.wantErr)
			assert.False(t, store.IsActive(ownerID))
			after, err := store.ListSessionNotices(t.Context(), ownerID,
				ListSessionNoticesOptions{})
			require.NoError(t, err)
			assert.Equal(t, before, after)
			turns, err := store.ListTurns(t.Context(), ownerID, ListTurnsOptions{})
			require.NoError(t, err)
			assert.Empty(t, turns.Items)
			messages, err := store.ListMessages(t.Context(), ownerID,
				ListMessagesOptions{})
			require.NoError(t, err)
			assert.Empty(t, messages.Items)
			lease, err := store.AcquireTurn(t.Context(), ownerID, StartTurnInput{
				RequestID: uuid.New(), Workspace: testWorkspace,
			})
			require.NoError(t, err)
			store.ReleaseTurn(lease)
			page, err := store.ListSessionNotices(t.Context(), foreign.ID,
				ListSessionNoticesOptions{})
			require.NoError(t, err)
			require.Len(t, page.Items, 1)
			assert.Equal(t, models.NoticeStatePending, page.Items[0].State)
		})
	}
}

func TestTurnNoticeAdmissionRollsBackWithTranscriptFailure(t *testing.T) {
	t.Parallel()
	store, ownerID, noticeID := openingNoticeFixture(t)
	messageID := uuid.New()
	_, err := store.AcquireTurn(t.Context(), ownerID, StartTurnInput{
		RequestID: uuid.New(), Workspace: testWorkspace,
		NoticeIDs: []uuid.UUID{noticeID},
		Messages: []MessageInput{
			{ID: messageID, Role: models.MessageRoleUser, Content: "first"},
			{ID: messageID, Role: models.MessageRoleUser, Content: "duplicate"},
		},
	})
	require.ErrorIs(t, err, commerr.ErrAlreadyExists)
	assert.False(t, store.IsActive(ownerID))
	page, err := store.ListSessionNotices(t.Context(), ownerID,
		ListSessionNoticesOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.NoticeStatePending, page.Items[0].State)
	assert.Nil(t, page.Items[0].DeliveredAt)
	turns, err := store.ListTurns(t.Context(), ownerID, ListTurnsOptions{})
	require.NoError(t, err)
	assert.Empty(t, turns.Items)
}

func TestTurnNoticeAdmissionRollsBackFailedOpeningLink(t *testing.T) {
	t.Parallel()
	store, ownerID, noticeID := openingNoticeFixture(t)
	old, err := store.AcquireTurn(t.Context(), ownerID, StartTurnInput{
		RequestID: uuid.New(), Workspace: testWorkspace,
	})
	require.NoError(t, err)
	require.NoError(t, store.query.TurnOpeningNotice.WithContext(t.Context()).
		Create(&models.TurnOpeningNotice{
			SessionID: ownerID, TurnID: old.TurnID,
			NoticeID: noticeID, SourceSequence: 1,
		}))
	store.ReleaseTurn(old)
	_, err = store.AcquireTurn(t.Context(), ownerID, StartTurnInput{
		RequestID: uuid.New(), Workspace: testWorkspace,
		NoticeIDs: []uuid.UUID{noticeID},
		Messages:  []MessageInput{{Role: models.MessageRoleUser, Content: "ready"}},
	})
	require.ErrorIs(t, err, commerr.ErrAlreadyExists)
	assert.False(t, store.IsActive(ownerID))
	page, err := store.ListSessionNotices(t.Context(), ownerID,
		ListSessionNoticesOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.NoticeStatePending, page.Items[0].State)
	assert.Nil(t, page.Items[0].DeliveredAt)
	turns, err := store.ListTurns(t.Context(), ownerID, ListTurnsOptions{})
	require.NoError(t, err)
	require.Len(t, turns.Items, 1)
	assert.Equal(t, old.TurnID, turns.Items[0].ID)
	messages, err := store.ListMessages(t.Context(), ownerID,
		ListMessagesOptions{})
	require.NoError(t, err)
	assert.Empty(t, messages.Items)
}

func TestTurnNoticeAdmissionConsumesOnlySelectedRows(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	owner := newTestSession(t.Context(), t, fixture.store)
	ids := make([]uuid.UUID, 0, 3)
	for range cap(ids) {
		notice, err := fixture.store.CreateSessionNotice(t.Context(), owner.ID,
			CreateSessionNoticeInput{
				Type: "app.ready", Source: "example", Summary: "ready",
				DataJSON: `{}`, Delivery: models.NoticeDeliveryQueue,
			})
		require.NoError(t, err)
		ids = append(ids, notice.ID)
	}
	lease, err := fixture.store.AcquireTurn(t.Context(), owner.ID, StartTurnInput{
		RequestID: uuid.New(), Workspace: testWorkspace, NoticeIDs: ids[:2],
		Messages: []MessageInput{{Role: models.MessageRoleUser, Content: "ready"}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { fixture.store.ReleaseTurn(lease) })
	page, err := fixture.store.ListSessionNotices(t.Context(), owner.ID,
		ListSessionNoticesOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, len(ids))
	for index, notice := range page.Items {
		if index == 2 {
			assert.Equal(t, models.NoticeStatePending, notice.State)
			assert.Nil(t, notice.DeliveredAt)

			continue
		}
		assert.Equal(t, models.NoticeStateDelivered, notice.State)
		assert.NotNil(t, notice.DeliveredAt)
	}
	_, err = fixture.store.AcquireTurn(t.Context(), owner.ID,
		StartTurnInput{NoticeIDs: ids[2:]})
	require.ErrorIs(t, err, ErrSessionBusy)
	page, err = fixture.store.ListSessionNotices(t.Context(), owner.ID,
		ListSessionNoticesOptions{ID: &ids[2]})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.NoticeStatePending, page.Items[0].State)
}
