package session

import (
	"math"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchExecutionsWalksEveryPageWithoutResuming(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	fixture.store.clock = func() time.Time { return fixture.root.CreatedAt }
	expected := make([]uuid.UUID, 0, 13)
	for range 13 {
		child := executionChildForTest(t, fixture, fixture.root)
		expected = append(expected, child.ID)
	}
	sort.Slice(expected, func(left, right int) bool {
		return expected[left].String() > expected[right].String()
	})
	_, err := fixture.store.PauseExecution(t.Context(),
		fixture.sessionID, fixture.root.ID, executionTestPauseReason)
	require.NoError(t, err)
	testCases := []struct {
		offset  int
		count   int
		hasMore bool
	}{
		{offset: 0, count: 5, hasMore: true},
		{offset: 5, count: 5, hasMore: true},
		{offset: 10, count: 3},
		{offset: 13},
		{offset: 99},
	}
	for _, tc := range testCases {
		page, err := fixture.store.SearchExecutions(t.Context(),
			fixture.sessionID, SearchExecutionsOptions{
				ParentID: fixture.root.ID, State: models.ExecutionStatePaused,
				Text: executionTestPauseReason, Limit: 5, Offset: tc.offset,
			})
		require.NoError(t, err)
		require.Len(t, page.Items, tc.count)
		assert.Equal(t, tc.hasMore, page.HasMore)
		for index, item := range page.Items {
			assert.Equal(t, expected[tc.offset+index], item.ID)
			assert.Equal(t, models.ExecutionStatePaused, item.State)
			assert.EqualValues(t, 2, item.EventCount)
		}
	}
	stored, err := fixture.store.GetExecution(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, stored.State)
	assert.EqualValues(t, 2, stored.EventCount)
}

func TestSearchExecutionsFiltersTextAndTime(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	start := fixture.root.CreatedAt
	end := start.Add(time.Second)
	otherZone := start.In(time.FixedZone("exampleOffset", 3*60*60))
	testCases := []struct {
		name    string
		options SearchExecutionsOptions
		count   int
	}{
		{name: "task", options: SearchExecutionsOptions{Text: "EXAMPLE"}, count: 1},
		{
			name: "context", options: SearchExecutionsOptions{Text: "saved progress"},
			count: 1,
		},
		{name: "SQL text", options: SearchExecutionsOptions{Text: "' OR 1=1 --"}},
		{name: "literal wildcard", options: SearchExecutionsOptions{Text: "%"}},
		{
			name: "identity", options: SearchExecutionsOptions{ID: fixture.root.ID},
			count: 1,
		},
		{name: "wrong identity", options: SearchExecutionsOptions{ID: uuid.New()}},
		{name: "time included", options: SearchExecutionsOptions{
			FromTime: &otherZone, ToTime: &end,
		}, count: 1},
		{name: "exclusive time end", options: SearchExecutionsOptions{
			ToTime: &start,
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			page, err := fixture.store.SearchExecutions(t.Context(),
				fixture.sessionID, tc.options)
			require.NoError(t, err)
			assert.Len(t, page.Items, tc.count)
			assert.False(t, page.HasMore)
		})
	}
}

func TestSearchExecutionsRejectsInvalidFilters(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	start := fixture.root.CreatedAt
	end := start.Add(-time.Second)
	testCases := []struct {
		name    string
		options SearchExecutionsOptions
		wantErr error
	}{
		{
			name: "negative offset", options: SearchExecutionsOptions{Offset: -1},
			wantErr: ErrInvalidPage,
		},
		{
			name: "negative limit", options: SearchExecutionsOptions{Limit: -1},
			wantErr: ErrInvalidPage,
		},
		{name: "oversized limit", options: SearchExecutionsOptions{
			Limit: MaximumPageLimit + 1,
		}, wantErr: ErrInvalidPage},
		{
			name: "state", options: SearchExecutionsOptions{
				State: models.ExecutionState("unknown"),
			},
			wantErr: commerr.ErrValidationFailed,
		},
		{name: "reversed times", options: SearchExecutionsOptions{
			FromTime: &start, ToTime: &end,
		}, wantErr: commerr.ErrValidationFailed},
		{name: "empty time interval", options: SearchExecutionsOptions{
			FromTime: &start, ToTime: &start,
		}, wantErr: commerr.ErrValidationFailed},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := fixture.store.SearchExecutions(t.Context(),
				fixture.sessionID, tc.options)
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestExecutionEventsWalkEveryPage(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	for range 6 {
		_, err := fixture.store.PauseExecution(t.Context(),
			fixture.sessionID, fixture.root.ID, executionTestPauseReason)
		require.NoError(t, err)
		_, err = fixture.store.ResumeExecution(t.Context(),
			fixture.sessionID, fixture.root.ID)
		require.NoError(t, err)
	}
	sequences := make([]int64, 0, 13)
	for _, offset := range []int{0, 5, 10, 13, 99} {
		page, err := fixture.store.ListExecutionEvents(t.Context(),
			fixture.sessionID, fixture.root.ID,
			ListEventsOptions{Limit: 5, Offset: offset})
		require.NoError(t, err)
		assert.Equal(t, offset+5 < 13, page.HasMore)
		for _, event := range page.Items {
			sequences = append(sequences, event.Sequence)
		}
	}
	assert.Equal(t, []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13},
		sequences)
}

func TestExecutionReadsDoNotOverflowThePageProbe(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	page, err := fixture.store.SearchExecutions(t.Context(), fixture.sessionID,
		SearchExecutionsOptions{Offset: math.MaxInt})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	assert.False(t, page.HasMore)
	history, err := fixture.store.ListExecutionEvents(t.Context(),
		fixture.sessionID, fixture.root.ID, ListEventsOptions{Offset: math.MaxInt})
	require.NoError(t, err)
	assert.Empty(t, history.Items)
	assert.False(t, history.HasMore)
}
