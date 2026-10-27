package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/essessey"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const turnStreamTestDeltas = 200

type countingCheckpointStore struct {
	session.Storage

	mutex  sync.Mutex
	writes int
	events []session.EventInput
}

func (s *countingCheckpointStore) AppendCheckpoint(
	_ context.Context,
	_ session.Lease,
	_ []session.MessageInput,
	events []session.EventInput,
) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.writes++
	s.events = append(s.events, events...)

	return nil
}

func (s *countingCheckpointStore) stored() (int, []session.EventInput) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	return s.writes, append([]session.EventInput(nil), s.events...)
}

func turnStreamTestEvent(eventType essessey.EventType, index int) Event {
	payload, _ := json.Marshal(map[string]int{"index": index}) //nolint:errchkjson // A map of ints always encodes.

	return Event{Type: eventType, Payload: payload}
}

func TestRuntimeTurnBatchesStreamDeltasInOrder(t *testing.T) {
	t.Parallel()
	store := &countingCheckpointStore{}
	var published []Event
	turn := &runtimeTurn{store: store, sink: func(event Event) error {
		published = append(published, event)

		return nil
	}}
	recorded := make([]Event, 0, turnStreamTestDeltas+2)
	recorded = append(recorded,
		turnStreamTestEvent(essessey.EventTypeContentBlockStart, 0))
	for index := range turnStreamTestDeltas {
		recorded = append(recorded,
			turnStreamTestEvent(essessey.EventTypeContentBlockDelta, index))
	}
	recorded = append(recorded,
		turnStreamTestEvent(essessey.EventTypeContentBlockStop, 0))
	for _, event := range recorded {
		require.NoError(t, turn.record(t.Context(), event))
	}
	assert.Equal(t, recorded, published)
	writes, stored := store.stored()
	require.Len(t, stored, len(recorded))
	for index, event := range recorded {
		assert.Equal(t, event.Type, stored[index].EventType)
		assert.JSONEq(t, string(event.Payload), stored[index].PayloadJSON)
	}
	assert.Less(t, writes, len(recorded)/10)
}

func TestRuntimeTurnStopsTheStreamAtARefusedDelta(t *testing.T) {
	t.Parallel()
	store := &countingCheckpointStore{}
	refused := ctxerrors.Wrap(commerr.ErrCancelled, "client went away")
	deltas := 0
	turn := &runtimeTurn{store: store, sink: func(event Event) error {
		if event.Type != essessey.EventTypeContentBlockDelta {
			return nil
		}

		deltas++
		if deltas == 2 {
			return refused
		}

		return nil
	}}
	require.NoError(t, turn.record(t.Context(),
		turnStreamTestEvent(essessey.EventTypeContentBlockStart, 0)))
	for index := range 3 {
		require.NoError(t, turn.record(t.Context(),
			turnStreamTestEvent(essessey.EventTypeContentBlockDelta, index)))
	}
	err := turn.record(t.Context(),
		turnStreamTestEvent(essessey.EventTypeContentBlockStop, 0))
	require.ErrorIs(t, err, commerr.ErrCancelled)
	_, stored := store.stored()
	for _, event := range stored {
		assert.NotEqual(t, essessey.EventTypeContentBlockStop, event.EventType)
	}
}
