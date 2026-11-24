package control_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFeedSubscribersFilterAndCancelIndependently(t *testing.T) {
	t.Parallel()
	store := newFakeFeedStore()
	pump := control.NewFeedPump(t.Context(), store)
	sessionID, requestID := uuid.New(), uuid.New()
	selected, stopSelected, err := pump.Subscribe(control.FeedFilter{
		SessionID: sessionID, RequestID: requestID,
	})
	require.NoError(t, err)
	t.Cleanup(stopSelected)
	all, stopAll, err := pump.Subscribe(control.FeedFilter{SessionID: sessionID})
	require.NoError(t, err)
	t.Cleanup(stopAll)
	store.append(sessionID, feedEvent(1, uuid.New()), feedEvent(2, requestID))
	otherSession := uuid.New()
	store.append(otherSession, feedEvent(1, requestID))
	pump.Signal(otherSession, 1)
	pump.Signal(sessionID, 1)
	page := receiveFeedPage(t, selected)
	require.Len(t, page, 1)
	assert.EqualValues(t, 2, page[0].Sequence)
	assert.Len(t, receiveFeedPage(t, all), 2)
	stopSelected()
	_, open := <-selected
	assert.False(t, open)
	stopSelected()
	store.append(sessionID, feedEvent(3, requestID))
	pump.Signal(sessionID, 3)
	assert.EqualValues(t, 3, receiveFeedPage(t, all)[0].Sequence)
}

func TestSlowFeedSubscriberCannotBlockTheSink(t *testing.T) {
	t.Parallel()
	store := newFakeFeedStore()
	pump := control.NewFeedPump(t.Context(), store)
	sink := &recordingSink{}
	pump.SetSink(sink.deliver)
	sessionID := uuid.New()
	pages, stop, err := pump.Subscribe(control.FeedFilter{SessionID: sessionID})
	require.NoError(t, err)
	t.Cleanup(stop)
	const pageCount = 32
	for i := range pageCount {
		sequence := int64(i + 1)
		store.append(sessionID, feedEvent(sequence, uuid.New()))
		pump.Signal(sessionID, sequence)
		require.Eventually(t, func() bool {
			return len(sink.sequences(t)) == i+1
		}, feedPumpWait, time.Millisecond)
	}
	for {
		select {
		case _, open := <-pages:
			if !open {
				assert.Equal(t, pageCount, len(sink.sequences(t)))

				return
			}
		case <-time.After(feedPumpWait):
			t.Fatal("overflowed subscriber was not disconnected")
		}
	}
}

func TestFeedSubscriberLifetimeAndValidation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	pump := control.NewFeedPump(ctx, newFakeFeedStore())
	_, _, err := pump.Subscribe(control.FeedFilter{})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	pages, stop, err := pump.Subscribe(control.FeedFilter{SessionID: uuid.New()})
	require.NoError(t, err)
	t.Cleanup(stop)
	cancel()
	select {
	case _, open := <-pages:
		assert.False(t, open)
	case <-time.After(feedPumpWait):
		t.Fatal("stopped feed kept its subscriber open")
	}
	_, _, err = pump.Subscribe(control.FeedFilter{SessionID: uuid.New()})
	require.ErrorIs(t, err, context.Canceled)
}

func receiveFeedPage(
	t *testing.T,
	pages <-chan []session.FeedItem,
) []session.FeedItem {
	t.Helper()
	select {
	case page, open := <-pages:
		require.True(t, open)

		return page
	case <-time.After(feedPumpWait):
		t.Fatal("feed subscription received nothing")

		return nil
	}
}
