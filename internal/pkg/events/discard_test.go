package events

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBusDiscardPreservesUnacknowledgedNotices(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		selected  []int
		remaining []int
	}{
		{name: "empty", remaining: []int{0, 1, 2}},
		{name: "missing", selected: []int{3}, remaining: []int{0, 1, 2}},
		{name: "middle", selected: []int{1}, remaining: []int{0, 2}},
		{name: "repeated", selected: []int{1, 1}, remaining: []int{0, 2}},
		{name: "all", selected: []int{0, 1, 2}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bus := NewBus(Options{})
			sessionID, other := uuid.New(), uuid.New()
			ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()}
			for _, id := range ids[:3] {
				notice := testNotice(sessionID, TypeAgentQuestion)
				notice.ID = id
				_, err := bus.Publish(notice)
				require.NoError(t, err)
			}
			foreign := testNotice(other, TypeAgentQuestion)
			foreign.ID = ids[1]
			_, err := bus.Publish(foreign)
			require.NoError(t, err)
			selected := make([]uuid.UUID, 0, len(tc.selected))
			for _, index := range tc.selected {
				selected = append(selected, ids[index])
			}
			bus.Discard(sessionID, selected)
			bus.Discard(uuid.New(), selected)
			assert.Equal(t, len(tc.remaining), bus.Pending(sessionID))
			batch := bus.Drain(sessionID)
			require.Len(t, batch.Notices, len(tc.remaining))
			for index, original := range tc.remaining {
				assert.Equal(t, ids[original], batch.Notices[index].ID)
			}
			foreignBatch := bus.Drain(other)
			require.Len(t, foreignBatch.Notices, 1)
			assert.Equal(t, foreign.ID, foreignBatch.Notices[0].ID)
		})
	}
}

func TestBusDiscardPreservesOverflowCount(t *testing.T) {
	t.Parallel()
	bus := NewBus(Options{MaxPendingPerSession: 2})
	sessionID := uuid.New()
	ids := make([]uuid.UUID, 0, 3)
	for range cap(ids) {
		notice, err := bus.Publish(testNotice(sessionID, TypeAgentQuestion))
		require.NoError(t, err)
		ids = append(ids, notice.ID)
	}
	bus.Discard(sessionID, ids[:2])
	batch := bus.Drain(sessionID)
	require.Len(t, batch.Notices, 1)
	assert.Equal(t, ids[2], batch.Notices[0].ID)
	assert.Equal(t, 1, batch.Dropped)
}
