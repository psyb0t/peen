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

func deferredReadFixture(
	t *testing.T,
	count int,
) (executionFixture, []uuid.UUID) {
	t.Helper()
	fixture, hook := sealedCompletionFixture(t)
	lease := deferredTestLease(t, fixture)
	ids := make([]uuid.UUID, 0, count)
	for range count {
		input := inboxTestInput()
		accepted, err := fixture.store.DeferExecutionMessage(t.Context(),
			fixture.root.ID, lease, input)
		require.NoError(t, err)
		ids = append(ids, accepted.Message.ID)
	}

	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, hook.Lease.GenerationID,
		finishActivityTestInput())
	require.NoError(t, err)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, finishActivityTestResult())
	require.NoError(t, err)

	return fixture, ids
}

func TestDeferredReadsWalkEveryPageInAcceptedOrder(t *testing.T) {
	t.Parallel()
	fixture, ids := deferredReadFixture(t, 13)
	testCases := []struct {
		offset  int
		count   int
		hasMore bool
	}{
		{offset: 0, count: 5, hasMore: true},
		{offset: 5, count: 5, hasMore: true},
		{offset: 10, count: 3},
		{offset: 13},
		{offset: 100},
	}
	for _, tc := range testCases {
		t.Run(strconv.Itoa(tc.offset), func(t *testing.T) {
			t.Parallel()
			page, err := fixture.store.ListDeferredExecutionMessages(t.Context(),
				fixture.sessionID, ListEventsOptions{Limit: 5, Offset: tc.offset})
			require.NoError(t, err)
			require.Len(t, page.Items, tc.count)
			assert.Equal(t, tc.hasMore, page.HasMore)
			for index, row := range page.Items {
				assert.Equal(t, ids[tc.offset+index], row.ID)
			}
		})
	}
}

func TestDeferredReadsExcludeUnfinishedAndPausedSources(t *testing.T) {
	t.Parallel()
	fixture, _ := sealedCompletionFixture(t)
	_, err := fixture.store.DeferExecutionMessage(t.Context(), fixture.root.ID,
		deferredTestLease(t, fixture), inboxTestInput())
	require.NoError(t, err)
	page, err := fixture.store.ListDeferredExecutionMessages(t.Context(),
		fixture.sessionID, ListEventsOptions{})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	_, err = fixture.store.PauseExecution(t.Context(), fixture.sessionID,
		fixture.root.ID, "leave source paused")
	require.NoError(t, err)
	before, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		fixture.root.ID)
	require.NoError(t, err)
	page, err = fixture.store.ListDeferredExecutionMessages(t.Context(),
		fixture.sessionID, ListEventsOptions{})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	after, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.Equal(t, models.ExecutionStatePaused, after.State)
}

func TestDeferredReadsValidateSessionAndPage(t *testing.T) {
	t.Parallel()
	fixture, _ := deferredReadFixture(t, 13)
	_, err := fixture.store.ListDeferredExecutionMessages(t.Context(),
		uuid.New(), ListEventsOptions{})
	require.ErrorIs(t, err, commerr.ErrNotFound)
	_, err = fixture.store.ListDeferredExecutionMessages(t.Context(),
		fixture.sessionID, ListEventsOptions{Offset: -1})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

func TestDeferredReadsDatasetBoundaries(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		count int
	}{
		{name: "empty"},
		{name: "single", count: 1},
		{name: "exact full page", count: 5},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, ids := deferredReadFixture(t, tc.count)
			page, err := fixture.store.ListDeferredExecutionMessages(t.Context(),
				fixture.sessionID, ListEventsOptions{Limit: 5})
			require.NoError(t, err)
			require.Len(t, page.Items, tc.count)
			assert.False(t, page.HasMore)
			assert.Equal(t, 5, page.Limit)
			assert.Zero(t, page.Offset)
			for index, row := range page.Items {
				assert.Equal(t, ids[index], row.ID)
			}
		})
	}
}

func TestDeferredReadsLimitContract(t *testing.T) {
	t.Parallel()
	fixture, _ := deferredReadFixture(t, 13)
	testCases := []struct {
		name      string
		limit     int
		wantLimit int
		wantErr   error
	}{
		{name: "default", wantLimit: DefaultPageLimit},
		{
			name: "maximum", limit: MaximumPageLimit,
			wantLimit: MaximumPageLimit,
		},
		{
			name: "oversized", limit: MaximumPageLimit + 1,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "negative", limit: -1,
			wantErr: commerr.ErrValidationFailed,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			page, err := fixture.store.ListDeferredExecutionMessages(t.Context(),
				fixture.sessionID, ListEventsOptions{Limit: tc.limit})
			require.ErrorIs(t, err, tc.wantErr)
			if tc.wantErr != nil {
				assert.Nil(t, page)

				return
			}

			require.Len(t, page.Items, 13)
			assert.Equal(t, tc.wantLimit, page.Limit)
			assert.False(t, page.HasMore)
		})
	}
}
