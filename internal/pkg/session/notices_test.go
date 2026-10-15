package session

import (
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoticeStateFilterPagesPendingHistory(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	create := func(count int) []uuid.UUID {
		t.Helper()
		ids := make([]uuid.UUID, 0, count)
		for range count {
			notice, err := fixture.store.CreateSessionNotice(t.Context(),
				fixture.sessionID, CreateSessionNoticeInput{
					Type: "app.changed", Source: "example", Summary: "changed",
					DataJSON: `{}`, Delivery: models.NoticeDeliveryQueue,
				})
			require.NoError(t, err)
			ids = append(ids, notice.ID)
		}

		return ids
	}
	delivered := create(3)
	_, err := fixture.store.DrainSessionNotices(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	pending := create(13)
	var got []uuid.UUID
	for index, count := range []int{5, 5, 3, 0, 0} {
		offset := index * 5
		if index == 3 {
			offset = len(pending)
		}
		page, err := fixture.store.ListSessionNotices(t.Context(), fixture.sessionID,
			ListSessionNoticesOptions{
				State: models.NoticeStatePending, Limit: 5, Offset: offset,
			})
		require.NoError(t, err)
		require.Len(t, page.Items, count)
		assert.Equal(t, index < 2, page.HasMore)
		for _, item := range page.Items {
			got = append(got, item.ID)
			assert.Equal(t, models.NoticeStatePending, item.State)
		}
	}
	assert.Equal(t, pending, got)
	page, err := fixture.store.ListSessionNotices(t.Context(), fixture.sessionID,
		ListSessionNoticesOptions{State: models.NoticeStateDelivered})
	require.NoError(t, err)
	require.Len(t, page.Items, len(delivered))
	for index, item := range page.Items {
		assert.Equal(t, delivered[index], item.ID)
	}
	_, err = fixture.store.ListSessionNotices(t.Context(), fixture.sessionID,
		ListSessionNoticesOptions{State: "unknown"})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

func TestNoticeIdentityFilter(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	ids := make([]uuid.UUID, 0, 3)
	for range cap(ids) {
		notice, err := fixture.store.CreateSessionNotice(t.Context(),
			fixture.sessionID, CreateSessionNoticeInput{
				Type: "app.changed", Source: "example", Summary: "changed",
				DataJSON: `{}`, Delivery: models.NoticeDeliveryQueue,
			})
		require.NoError(t, err)
		ids = append(ids, notice.ID)
	}
	selected := ids[1]
	page, err := fixture.store.ListSessionNotices(t.Context(), fixture.sessionID,
		ListSessionNoticesOptions{ID: &selected})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, selected, page.Items[0].ID)
	assert.False(t, page.HasMore)
	missing := uuid.New()
	page, err = fixture.store.ListSessionNotices(t.Context(), fixture.sessionID,
		ListSessionNoticesOptions{ID: &missing})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	page, err = fixture.store.ListSessionNotices(t.Context(), fixture.sessionID,
		ListSessionNoticesOptions{})
	require.NoError(t, err)
	assert.Len(t, page.Items, len(ids))
	for _, offset := range []int{1, 100} {
		page, err = fixture.store.ListSessionNotices(t.Context(), fixture.sessionID,
			ListSessionNoticesOptions{ID: &selected, Offset: offset})
		require.NoError(t, err)
		assert.Empty(t, page.Items)
		assert.False(t, page.HasMore)
	}
	other := newTestSession(t.Context(), t, fixture.store)
	page, err = fixture.store.ListSessionNotices(t.Context(), other.ID,
		ListSessionNoticesOptions{ID: &selected})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}

func TestNoticeStateFilterDatasetBoundaries(t *testing.T) {
	t.Parallel()
	for _, count := range []int{0, 1, 5} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionFixture(t)
			for range count {
				_, err := fixture.store.CreateSessionNotice(t.Context(),
					fixture.sessionID, CreateSessionNoticeInput{
						Type: "app.changed", Source: "example", Summary: "changed",
						DataJSON: `{}`, Delivery: models.NoticeDeliveryQueue,
					})
				require.NoError(t, err)
			}
			for _, offset := range []int{0, count, 100} {
				page, err := fixture.store.ListSessionNotices(t.Context(),
					fixture.sessionID, ListSessionNoticesOptions{
						State: models.NoticeStatePending, Limit: 5, Offset: offset,
					})
				require.NoError(t, err)
				want := 0
				if offset == 0 {
					want = count
				}
				assert.Len(t, page.Items, want)
				assert.False(t, page.HasMore)
			}
		})
	}
}
