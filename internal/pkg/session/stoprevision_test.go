package session

import (
	"math"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStopRevisionRefusesPreviouslyAcceptedOpening(t *testing.T) {
	t.Parallel()
	store, sessionID, noticeID := openingNoticeFixture(t)
	revision := int64(0)
	input := StartTurnInput{
		RequestID: uuid.New(), Workspace: testWorkspace,
		ExpectedStopRevision: &revision, NoticeIDs: []uuid.UUID{noticeID},
		Messages: []MessageInput{{Role: models.MessageRoleUser, Content: "old work"}},
	}
	active, err := store.Cancel(t.Context(), sessionID)
	require.NoError(t, err)
	assert.False(t, active)
	_, err = store.AcquireTurn(t.Context(), sessionID, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	assert.False(t, store.IsActive(sessionID))
	notices, err := store.ListSessionNotices(t.Context(), sessionID,
		ListSessionNoticesOptions{State: models.NoticeStatePending})
	require.NoError(t, err)
	require.Len(t, notices.Items, 1)
	assert.Equal(t, noticeID, notices.Items[0].ID)
	messages, err := store.ListMessages(t.Context(), sessionID,
		ListMessagesOptions{})
	require.NoError(t, err)
	assert.Empty(t, messages.Items)
	revision = 1
	lease, err := store.AcquireTurn(t.Context(), sessionID, input)
	require.NoError(t, err)
	store.ReleaseTurn(lease)
}

func TestStopRevisionSurvivesSQLiteReopen(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	fixture.store.ReleaseTurn(inboxTestLease(t, fixture))
	for i := range 3 {
		stopped, err := fixture.store.Cancel(t.Context(), fixture.sessionID)
		require.NoError(t, err)
		assert.Equal(t, i == 0, stopped)
	}
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store := newTestStore(t, handle)
	stored, err := store.Get(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	assert.EqualValues(t, 3, stored.StopRevision)
	stale := int64(2)
	_, err = store.AcquireTurn(t.Context(), fixture.sessionID, StartTurnInput{
		RequestID: uuid.New(), Workspace: testWorkspace, ExpectedStopRevision: &stale,
	})
	require.ErrorIs(t, err, commerr.ErrConflict)
	lease, err := store.AcquireTurn(t.Context(), fixture.sessionID, StartTurnInput{
		RequestID: uuid.New(), Workspace: testWorkspace,
		ExpectedStopRevision: &stored.StopRevision,
	})
	require.NoError(t, err)
	store.ReleaseTurn(lease)
}

func TestStopRevisionValidatesExpectedValue(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		revision int64
		wantErr  error
	}{
		{name: "current"},
		{name: "negative", revision: -1, wantErr: commerr.ErrValidationFailed},
		{name: "future", revision: 1, wantErr: commerr.ErrConflict},
		{name: "maximum", revision: math.MaxInt64, wantErr: commerr.ErrConflict},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, sessionID, _ := openingNoticeFixture(t)
			lease, err := store.AcquireTurn(t.Context(), sessionID, StartTurnInput{
				RequestID: uuid.New(), Workspace: testWorkspace,
				ExpectedStopRevision: &tc.revision,
			})
			require.ErrorIs(t, err, tc.wantErr)
			if tc.wantErr == nil {
				store.ReleaseTurn(lease)

				return
			}
			assert.False(t, store.IsActive(sessionID))
			turns := store.query.Turn
			count, err := turns.WithContext(t.Context()).Where(
				turns.SessionID.Eq(sessionID)).Count()
			require.NoError(t, err)
			assert.Zero(t, count)
		})
	}
}

func TestStopRevisionOverflowRollsBack(t *testing.T) {
	t.Parallel()
	store, sessionID, _ := openingNoticeFixture(t)
	repository := store.query.Session
	_, err := repository.WithContext(t.Context()).Where(repository.ID.Eq(sessionID)).
		UpdateSimple(repository.StopRevision.Value(math.MaxInt64))
	require.NoError(t, err)
	_, err = store.Cancel(t.Context(), sessionID)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	stored, err := store.Get(t.Context(), sessionID)
	require.NoError(t, err)
	assert.Equal(t, int64(math.MaxInt64), stored.StopRevision)
}

func TestStopRevisionConcurrentIdleStops(t *testing.T) {
	t.Parallel()
	store, sessionID, _ := openingNoticeFixture(t)
	const count = 8
	var workers sync.WaitGroup
	failures := make(chan error, count)
	for range count {
		workers.Go(func() {
			_, err := store.Cancel(t.Context(), sessionID)
			failures <- err
		})
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	stored, err := store.Get(t.Context(), sessionID)
	require.NoError(t, err)
	assert.EqualValues(t, count, stored.StopRevision)
}

func TestDeferredAdmissionRemainsStoppedAfterNewSessionRevision(t *testing.T) {
	t.Parallel()
	fixture, ids := deferredReadFixture(t, 1)
	fixture.store.ReleaseTurn(deferredTestLease(t, fixture))
	page, err := fixture.store.ListDeferredExecutionMessages(t.Context(),
		fixture.sessionID, ListEventsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	message := page.Items[0]
	assert.Zero(t, message.StopRevision)
	_, err = fixture.store.Cancel(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	_, err = fixture.store.AcquireDeferredExecutionTurn(t.Context(),
		fixture.sessionID, ids[0], StartTurnInput{
			RequestID: message.RequestID,
			Workspace: message.WorkingDirectory,
		})
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	assert.False(t, fixture.store.IsActive(fixture.sessionID))
	after, err := fixture.store.ListDeferredExecutionMessages(t.Context(),
		fixture.sessionID, ListEventsOptions{})
	require.NoError(t, err)
	assert.Equal(t, page, after)
}
