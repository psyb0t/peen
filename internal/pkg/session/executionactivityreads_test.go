package session

import (
	"fmt"
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutionActivityPagesPreservePausedState(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	ctx := t.Context()
	ids := make([]uuid.UUID, 0, 13)
	for index := range 13 {
		input := activityTestInput(models.ActivityKindModel)
		input.StepID = fmt.Sprintf("model%d", index)
		claim, err := fixture.store.BeginExecutionActivity(ctx,
			fixture.sessionID, fixture.root.ID, uuid.New(), input)
		require.NoError(t, err)
		ids = append(ids, claim.Activity.ID)
	}
	_, err := fixture.store.PauseExecution(ctx, fixture.sessionID,
		fixture.root.ID, activityTestEvidence)
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
		{offset: 100},
		{offset: math.MaxInt},
	}
	for _, tc := range testCases {
		page, err := fixture.store.ListExecutionActivities(ctx,
			fixture.sessionID, fixture.root.ID,
			ListEventsOptions{Limit: 5, Offset: tc.offset})
		require.NoError(t, err)
		require.Len(t, page.Items, tc.count)
		assert.Equal(t, tc.hasMore, page.HasMore)
		for index, activity := range page.Items {
			assert.Equal(t, ids[tc.offset+index], activity.ID)
		}
	}
	root, err := fixture.store.GetExecution(ctx, fixture.sessionID,
		fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, root.State)
}

func TestExecutionActivityReadsRejectWrongSession(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	claim := beginActivityForTest(t, fixture, uuid.New(), models.ActivityKindModel)
	_, err := fixture.store.GetExecutionActivity(t.Context(), uuid.New(),
		claim.Activity.ID)
	require.ErrorIs(t, err, commerr.ErrNotFound)
	_, err = fixture.store.ListExecutionActivities(t.Context(), uuid.New(),
		fixture.root.ID, ListEventsOptions{})
	require.ErrorIs(t, err, commerr.ErrNotFound)
}
