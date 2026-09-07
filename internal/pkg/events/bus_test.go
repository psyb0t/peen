package events

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testSummary          = "go test ./... exited 1"
	testSource           = "job:0b7f1c96"
	testConcurrentWriter = 8
	testWritesPerWriter  = 32
)

func testNotice(sessionID uuid.UUID, eventType Type) Notice {
	return Notice{
		SessionID: sessionID,
		Type:      eventType,
		Source:    testSource,
		Summary:   testSummary,
	}
}

func TestBusPublishAndDrain(t *testing.T) {
	t.Parallel()

	bus := NewBus(Options{})
	session := uuid.New()

	published, err := bus.Publish(testNotice(session, TypeJobExited))
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, published.ID, "an ID is assigned")
	assert.False(t, published.CreatedAt.IsZero(), "a timestamp is assigned")
	assert.Equal(t, DeliveryQueue, published.Delivery, "queue is the default")

	assert.Equal(t, 1, bus.Pending(session))

	batch := bus.Drain(session)
	require.Len(t, batch.Notices, 1)
	assert.Equal(t, 0, batch.Dropped)
	assert.Equal(t, published.ID, batch.Notices[0].ID)

	assert.Equal(t, 0, bus.Pending(session), "a drain consumes")
	assert.Empty(t, bus.Drain(session).Notices, "draining twice is empty")
}

// A burst has to come back in one batch so the turn can coalesce it into a
// single message rather than one message per event.
func TestBusDrainReturnsWholeBurstInOrder(t *testing.T) {
	t.Parallel()

	bus := NewBus(Options{})
	session := uuid.New()

	const burst = 5

	for index := range burst {
		notice := testNotice(session, TypeJobExited)
		notice.Summary = strconv.Itoa(index)
		_, err := bus.Publish(notice)
		require.NoError(t, err)
	}

	batch := bus.Drain(session)
	require.Len(t, batch.Notices, burst)

	for index, notice := range batch.Notices {
		assert.Equal(t, strconv.Itoa(index), notice.Summary)
	}
}

// A runaway producer must not exhaust memory or bury the newest event, so the
// queue drops oldest and says how many it dropped.
func TestBusDropsOldestOverTheBoundAndCountsIt(t *testing.T) {
	t.Parallel()

	const bound = 3

	bus := NewBus(Options{MaxPendingPerSession: bound})
	session := uuid.New()

	const published = 10

	for index := range published {
		notice := testNotice(session, TypeJobExited)
		notice.Summary = strconv.Itoa(index)
		_, err := bus.Publish(notice)
		require.NoError(t, err)
	}

	batch := bus.Drain(session)
	require.Len(t, batch.Notices, bound)
	assert.Equal(t, published-bound, batch.Dropped)
	assert.Equal(
		t,
		[]string{"7", "8", "9"},
		[]string{
			batch.Notices[0].Summary,
			batch.Notices[1].Summary,
			batch.Notices[2].Summary,
		},
		"the newest events survive",
	)
}

func TestBusIsolatesSessions(t *testing.T) {
	t.Parallel()

	bus := NewBus(Options{})
	first := uuid.New()
	second := uuid.New()

	_, err := bus.Publish(testNotice(first, TypeJobExited))
	require.NoError(t, err)

	assert.Equal(t, 1, bus.Pending(first))
	assert.Equal(t, 0, bus.Pending(second))
	assert.Empty(t, bus.Drain(second).Notices)
	assert.Len(t, bus.Drain(first).Notices, 1)
}

func TestBusPublishValidates(t *testing.T) {
	t.Parallel()

	bus := NewBus(Options{})
	session := uuid.New()

	testCases := []struct {
		name    string
		notice  Notice
		wantErr error
	}{
		{
			name:    "missing session",
			notice:  Notice{Type: TypeJobExited},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name:    "malformed type",
			notice:  Notice{SessionID: session, Type: "Nope"},
			wantErr: ErrInvalidType,
		},
		{
			name: "unknown delivery",
			notice: Notice{
				SessionID: session,
				Type:      TypeJobExited,
				Delivery:  "explode",
			},
			wantErr: ErrInvalidDelivery,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := bus.Publish(tc.notice)
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

// Peen's own producers publish reserved types through the bus. Only the HTTP
// boundary applies the external rule.
func TestBusAcceptsReservedTypesFromInternalProducers(t *testing.T) {
	t.Parallel()

	bus := NewBus(Options{})
	session := uuid.New()

	_, err := bus.Publish(testNotice(session, TypeAgentFinished))
	require.NoError(t, err)
	assert.Equal(t, 1, bus.Pending(session))
}

func TestBusBoundsSummaryAndDropsOversizedData(t *testing.T) {
	t.Parallel()

	const summaryBound = 16

	bus := NewBus(Options{MaxSummaryBytes: summaryBound, MaxDataBytes: 8})
	session := uuid.New()

	notice := testNotice(session, TypeJobExited)
	notice.Summary = strings.Repeat("x", summaryBound*2)
	notice.Data = json.RawMessage(`{"a":"aaaaaaaaaaaaaaaaaaaa"}`)

	published, err := bus.Publish(notice)
	require.NoError(t, err)
	assert.Len(t, published.Summary, summaryBound)
	assert.Nil(t, published.Data, "oversized data is dropped, not truncated")
}

func TestBusSubscribeDeliversLiveEvents(t *testing.T) {
	t.Parallel()

	bus := NewBus(Options{})
	session := uuid.New()

	stream, stop := bus.Subscribe(session)
	defer stop()

	published, err := bus.Publish(testNotice(session, TypeJobExited))
	require.NoError(t, err)

	received := <-stream
	assert.Equal(t, published.ID, received.ID)

	_, err = bus.Publish(testNotice(uuid.New(), TypeJobExited))
	require.NoError(t, err)
	assert.Empty(t, stream, "another session's events do not arrive")
}

// A subscriber that stops reading must not stall a finishing process. It misses
// events; the durable queue is what guarantees delivery.
func TestBusPublishDoesNotBlockOnAFullSubscriber(t *testing.T) {
	t.Parallel()

	bus := NewBus(Options{SubscriberBuffer: 1})
	session := uuid.New()

	_, stop := bus.Subscribe(session)
	defer stop()

	const published = 20

	for range published {
		_, err := bus.Publish(testNotice(session, TypeJobExited))
		require.NoError(t, err)
	}

	assert.Equal(
		t,
		published,
		bus.Pending(session),
		"every event still reached the durable queue",
	)
}

func TestBusStopIsIdempotent(t *testing.T) {
	t.Parallel()

	bus := NewBus(Options{})
	session := uuid.New()

	stream, stop := bus.Subscribe(session)

	stop()
	stop()

	_, open := <-stream
	assert.False(t, open, "the channel is closed exactly once")

	_, err := bus.Publish(testNotice(session, TypeJobExited))
	require.NoError(t, err, "publishing after unsubscribe still works")
}

func TestBusConcurrentPublishAndDrain(t *testing.T) {
	t.Parallel()

	bus := NewBus(Options{MaxPendingPerSession: testConcurrentWriter *
		testWritesPerWriter})
	session := uuid.New()

	stream, stop := bus.Subscribe(session)
	defer stop()

	go func() {
		for range stream { //nolint:revive // Draining so fan-out never fills.
		}
	}()

	wg := sync.WaitGroup{}
	for range testConcurrentWriter {
		wg.Go(func() {
			for range testWritesPerWriter {
				_, err := bus.Publish(testNotice(session, TypeJobExited))
				assert.NoError(t, err)
			}
		})
	}

	wg.Wait()

	batch := bus.Drain(session)
	assert.Len(t, batch.Notices, testConcurrentWriter*testWritesPerWriter)
	assert.Equal(t, 0, batch.Dropped)
}
