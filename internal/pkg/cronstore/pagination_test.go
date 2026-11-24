package cronstore

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaginationWalkAndLatest(t *testing.T) {
	t.Parallel()
	store, _ := testStore(t)
	const count = 53
	for index := range count {
		claimed, err := store.ClaimRun(t.Context(),
			testRun("nightlyReport", int64(index+10)))
		require.NoError(t, err)
		require.True(t, claimed)
	}
	seen := make(map[uuid.UUID]struct{}, count)
	var previous int64
	for offset := 0; ; offset += 7 {
		rows, more, err := store.ListRuns(t.Context(),
			"nightlyReport", 7, offset)
		require.NoError(t, err)
		require.NotEmpty(t, rows)
		for _, row := range rows {
			_, duplicate := seen[row.ID]
			require.False(t, duplicate)
			seen[row.ID] = struct{}{}
			if previous != 0 {
				assert.Less(t, row.ScheduledAt.Unix(), previous)
			}
			previous = row.ScheduledAt.Unix()
		}
		if !more {
			break
		}
	}
	assert.Len(t, seen, count)
	last, err := store.LastRun(t.Context(), "nightlyReport")
	require.NoError(t, err)
	assert.EqualValues(t, count+9, last.ScheduledAt.Unix())
	defaults, more, err := store.ListRuns(t.Context(), "", 0, 0)
	require.NoError(t, err)
	assert.Len(t, defaults, 50)
	assert.True(t, more)
	empty, more, err := store.ListRuns(t.Context(), "otherJob", 7, 0)
	require.NoError(t, err)
	assert.Empty(t, empty)
	assert.False(t, more)
}

func TestPaginationBounds(t *testing.T) {
	t.Parallel()
	store, _ := testStore(t)
	testCases := []struct {
		limit, offset int
		name          string
	}{
		{limit: -1},
		{limit: 201},
		{limit: 10, offset: -1},
		{limit: 1, name: strings.Repeat("a", 65)},
	}
	for _, tc := range testCases {
		_, _, err := store.ListRuns(t.Context(), tc.name, tc.limit, tc.offset)
		require.ErrorIs(t, err, commerr.ErrValidationFailed)
	}
}

func TestGlobalRunOrderingBreaksTiesByID(t *testing.T) {
	t.Parallel()
	store, _ := testStore(t)
	first, second := testRun("firstJob", 10), testRun("secondJob", 10)
	for _, run := range []*models.CronRun{first, second} {
		claimed, err := store.ClaimRun(t.Context(), run)
		require.NoError(t, err)
		require.True(t, claimed)
	}
	rows, more, err := store.ListRuns(t.Context(), "", 1, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, more)
	expected := max(second.ID.String(), first.ID.String())
	assert.Equal(t, expected, rows[0].ID.String())
}
