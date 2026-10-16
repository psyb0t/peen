package control_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	feedPumpWait = 2 * time.Second
	// feedPumpQuiet is how long a test watches for something that must not
	// happen, such as a second delivery or a repeated admission.
	feedPumpQuiet = 50 * time.Millisecond
)

// fakeFeedStore answers ListFeed from an in-memory, per-session slice the
// test appends to, standing in for session.Store so the pump's own control
// flow is what each test proves rather than SQLite's.
type fakeFeedStore struct {
	mu    sync.Mutex
	items map[uuid.UUID][]session.FeedItem

	// calls counts ListFeed invocations per session, for the single-flight
	// assertion.
	calls map[uuid.UUID]int

	// hook runs inside ListFeed, after the call is counted and before the
	// page is built, so a test can block a read or inject an error.
	hook func(sessionID uuid.UUID, cursor int64)

	// failNext, when set for a session, is returned once and then cleared.
	failNext map[uuid.UUID]error
}

func newFakeFeedStore() *fakeFeedStore {
	return &fakeFeedStore{
		items:    map[uuid.UUID][]session.FeedItem{},
		calls:    map[uuid.UUID]int{},
		failNext: map[uuid.UUID]error{},
	}
}

func (f *fakeFeedStore) append(sessionID uuid.UUID, items ...session.FeedItem) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.items[sessionID] = append(f.items[sessionID], items...)
}

func (f *fakeFeedStore) failOnce(sessionID uuid.UUID, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.failNext[sessionID] = err
}

func (f *fakeFeedStore) callCount(sessionID uuid.UUID) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls[sessionID]
}

func (f *fakeFeedStore) ListFeed(
	_ context.Context,
	sessionID uuid.UUID,
	cursor int64,
	limit int,
) (*session.FeedPage, error) {
	f.mu.Lock()
	f.calls[sessionID]++
	hook := f.hook
	f.mu.Unlock()

	if hook != nil {
		hook(sessionID, cursor)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.failNext[sessionID]; err != nil {
		delete(f.failNext, sessionID)

		return nil, err
	}

	all := f.items[sessionID]

	var page []session.FeedItem
	for _, item := range all {
		if item.Sequence > cursor {
			page = append(page, item)
		}
	}

	hasMore := false
	if len(page) > limit {
		page = page[:limit]
		hasMore = true
	}

	nextCursor := cursor
	if len(page) > 0 {
		nextCursor = page[len(page)-1].Sequence
	}

	head := cursor
	if len(all) > 0 {
		head = all[len(all)-1].Sequence
	}

	return &session.FeedPage{
		Items:      page,
		NextCursor: nextCursor,
		HasMore:    hasMore,
		Head:       head,
	}, nil
}

func feedEvent(sequence int64, requestID uuid.UUID) session.FeedItem {
	return session.FeedItem{
		Sequence:  sequence,
		ID:        uuid.New(),
		Kind:      session.FeedKindEvent,
		EventType: agent.EventTypeUserMessageCreated,
		RequestID: requestID,
	}
}

type recordedDelivery struct {
	sessionID uuid.UUID
	items     []session.FeedItem
}

type recordingSink struct {
	mu    sync.Mutex
	pages []recordedDelivery
}

func (s *recordingSink) deliver(
	_ context.Context,
	sessionID uuid.UUID,
	items []session.FeedItem,
) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pages = append(s.pages, recordedDelivery{sessionID: sessionID, items: items})
}

func (s *recordingSink) sequences(t *testing.T) []int64 {
	t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()

	var out []int64
	for _, page := range s.pages {
		for _, item := range page.items {
			out = append(out, item.Sequence)
		}
	}

	return out
}

func (s *recordingSink) pageCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.pages)
}

// Every row a session commits reaches the sink in sequence order, even when
// later signals arrive redundantly or while an earlier read is still being
// handled. Because the store signals only after a write commits and SQLite
// writes serialize, one session's signals always announce its commits in
// commit order; what this proves is that a flurry of them, some landing
// before the pump has caught up, never reorders or skips a row.
func TestFeedPumpDeliversInSequenceOrderAcrossMultipleSignals(t *testing.T) {
	t.Parallel()

	store := newFakeFeedStore()
	pump := control.NewFeedPump(t.Context(), store)
	sink := &recordingSink{}
	pump.SetSink(sink.deliver)

	sessionID := uuid.New()

	// The first commit's signal establishes the session's cursor.
	store.append(sessionID, feedEvent(1, uuid.New()))
	pump.Signal(sessionID, 1)

	require.Eventually(t, func() bool {
		return len(sink.sequences(t)) >= 1
	}, feedPumpWait, time.Millisecond)

	// Two more commits land, and both signal before either read is known to
	// have finished.
	store.append(sessionID, feedEvent(2, uuid.New()), feedEvent(3, uuid.New()))
	pump.Signal(sessionID, 2)
	pump.Signal(sessionID, 3)

	require.Eventually(t, func() bool {
		return len(sink.sequences(t)) >= 3
	}, feedPumpWait, time.Millisecond)

	assert.Equal(t, []int64{1, 2, 3}, sink.sequences(t))
}

// Only one goroutine reads a session's feed at a time. A signal that arrives
// while one is already running marks the run dirty instead of starting a
// second reader.
func TestFeedPumpSingleFlightsPerSession(t *testing.T) {
	t.Parallel()

	store := newFakeFeedStore()
	sessionID := uuid.New()
	store.append(sessionID, feedEvent(1, uuid.New()))

	release := make(chan struct{})
	entered := make(chan struct{}, 1)

	var hookCalls atomic.Int32

	store.hook = func(hookSession uuid.UUID, _ int64) {
		if hookSession != sessionID {
			return
		}

		if hookCalls.Add(1) == 1 {
			entered <- struct{}{}
			<-release
		}
	}

	pump := control.NewFeedPump(t.Context(), store)
	sink := &recordingSink{}
	pump.SetSink(sink.deliver)

	pump.Signal(sessionID, 1)
	<-entered

	// These land while the first read is blocked inside the hook. They must
	// not start a second ListFeed call; a single-flight run picks up the
	// dirty flag and loops instead.
	pump.Signal(sessionID, 1)
	pump.Signal(sessionID, 1)

	close(release)

	require.Eventually(t, func() bool {
		return len(sink.sequences(t)) >= 1
	}, feedPumpWait, time.Millisecond)

	assert.Equal(t, []int64{1}, sink.sequences(t))
}

// A row committed while a read is in flight is not missed: the dirty flag set
// during that read makes the pump loop once more after the in-flight page
// finishes.
func TestFeedPumpDirtyReloopPicksUpRowsCommittedDuringARead(t *testing.T) {
	t.Parallel()

	store := newFakeFeedStore()
	sessionID := uuid.New()
	store.append(sessionID, feedEvent(1, uuid.New()))

	entered := make(chan struct{}, 1)
	proceed := make(chan struct{})

	var hookCalls atomic.Int32

	store.hook = func(hookSession uuid.UUID, _ int64) {
		if hookSession != sessionID {
			return
		}

		if hookCalls.Add(1) == 1 {
			entered <- struct{}{}
			<-proceed
		}
	}

	pump := control.NewFeedPump(t.Context(), store)
	sink := &recordingSink{}
	pump.SetSink(sink.deliver)

	pump.Signal(sessionID, 1)
	<-entered

	// Commit and signal row 2 while row 1's read is still blocked in the
	// hook, then let the first read finish.
	store.append(sessionID, feedEvent(2, uuid.New()))
	pump.Signal(sessionID, 2)
	close(proceed)

	require.Eventually(t, func() bool {
		return len(sink.sequences(t)) >= 2
	}, feedPumpWait, time.Millisecond)

	assert.Equal(t, []int64{1, 2}, sink.sequences(t))
}

// A read failure stops that run without losing the cursor. The next signal
// retries from the same place.
func TestFeedPumpRetriesAfterAReadError(t *testing.T) {
	t.Parallel()

	store := newFakeFeedStore()
	sessionID := uuid.New()
	store.append(sessionID, feedEvent(1, uuid.New()))
	store.failOnce(sessionID, ctxerrors.Wrap(commerr.ErrFetchFailed, "boom"))

	pump := control.NewFeedPump(t.Context(), store)
	sink := &recordingSink{}
	pump.SetSink(sink.deliver)

	pump.Signal(sessionID, 1)

	require.Eventually(t, func() bool {
		return store.callCount(sessionID) >= 1
	}, feedPumpWait, time.Millisecond)

	// The failed read delivered nothing.
	assert.Never(t, func() bool {
		return len(sink.sequences(t)) > 0
	}, feedPumpQuiet, time.Millisecond)

	pump.Signal(sessionID, 1)

	require.Eventually(t, func() bool {
		return len(sink.sequences(t)) >= 1
	}, feedPumpWait, time.Millisecond)
	assert.Equal(t, []int64{1}, sink.sequences(t))
}

// A watched message's admission fires exactly once, when the pump delivers
// the userMessage.created row for it, not before.
func TestFeedPumpReportsAdmissionOnceOnDelivery(t *testing.T) {
	t.Parallel()

	store := newFakeFeedStore()
	pump := control.NewFeedPump(t.Context(), store)
	sink := &recordingSink{}
	pump.SetSink(sink.deliver)

	sessionID := uuid.New()
	requestID := uuid.New()

	var admissions atomic.Int32
	stop := pump.WatchAdmission(sessionID, requestID, func() {
		admissions.Add(1)
	})
	t.Cleanup(stop)

	store.append(sessionID, feedEvent(1, requestID))
	pump.Signal(sessionID, 1)

	require.Eventually(t, func() bool {
		return admissions.Load() == 1
	}, feedPumpWait, time.Millisecond)

	// A repeat signal over the same committed row must not report again.
	pump.Signal(sessionID, 1)
	assert.Never(t, func() bool {
		return admissions.Load() != 1
	}, feedPumpQuiet, time.Millisecond)
}

// A stopped watch hears nothing, so a message that already finished cannot
// release a reservation it no longer holds.
func TestFeedPumpStoppedWatchHearsNothing(t *testing.T) {
	t.Parallel()

	store := newFakeFeedStore()
	pump := control.NewFeedPump(t.Context(), store)
	sink := &recordingSink{}
	pump.SetSink(sink.deliver)

	sessionID := uuid.New()
	requestID := uuid.New()

	var admissions atomic.Int32
	stop := pump.WatchAdmission(sessionID, requestID, func() {
		admissions.Add(1)
	})
	stop()

	store.append(sessionID, feedEvent(1, requestID))
	pump.Signal(sessionID, 1)

	require.Eventually(t, func() bool {
		return len(sink.sequences(t)) >= 1
	}, feedPumpWait, time.Millisecond)
	assert.Zero(t, admissions.Load())
}

// The first signal a session ever receives sets its cursor to first-1, so
// only rows from first onward are read, never the whole table.
func TestFeedPumpInitializesCursorFromTheFirstSignal(t *testing.T) {
	t.Parallel()

	store := newFakeFeedStore()
	sessionID := uuid.New()
	store.append(sessionID,
		feedEvent(1, uuid.New()),
		feedEvent(2, uuid.New()),
		feedEvent(3, uuid.New()),
	)

	pump := control.NewFeedPump(t.Context(), store)
	sink := &recordingSink{}
	pump.SetSink(sink.deliver)

	// Row 1 and 2 are already committed by the time this controller learns
	// about the session, for example a restart. The first signal names 3 as
	// the first sequence this commit allocated, so delivery starts there.
	pump.Signal(sessionID, 3)

	require.Eventually(t, func() bool {
		return len(sink.sequences(t)) >= 1
	}, feedPumpWait, time.Millisecond)

	assert.Never(t, func() bool {
		return len(sink.sequences(t)) != 1
	}, feedPumpQuiet, time.Millisecond)
	assert.Equal(t, []int64{3}, sink.sequences(t))
}

// Admission release must not wait on a live client: with no sink registered
// the pump still reads and releases the watch, and a sink registered later
// receives the rows committed after it arrived, in order.
func TestFeedPumpReleasesAdmissionWithNoSinkThenDeliversLaterRows(t *testing.T) {
	t.Parallel()

	store := newFakeFeedStore()
	sessionID := uuid.New()
	requestID := uuid.New()
	store.append(sessionID, feedEvent(1, requestID))

	pump := control.NewFeedPump(t.Context(), store)

	admitted := make(chan struct{})
	stop := pump.WatchAdmission(sessionID, requestID, func() {
		close(admitted)
	})
	t.Cleanup(stop)

	pump.Signal(sessionID, 1)

	select {
	case <-admitted:
	case <-time.After(feedPumpWait):
		require.FailNow(t, "admission was not released without a sink")
	}

	sink := &recordingSink{}
	pump.SetSink(sink.deliver)

	store.append(sessionID, feedEvent(2, uuid.New()))
	pump.Signal(sessionID, 2)

	require.Eventually(t, func() bool {
		return len(sink.sequences(t)) >= 1
	}, feedPumpWait, time.Millisecond)
	assert.Equal(t, []int64{2}, sink.sequences(t))
}

// A session with no rows waiting answers an empty page and stops; it does not
// spin.
func TestFeedPumpStopsWhenNothingIsPending(t *testing.T) {
	t.Parallel()

	store := newFakeFeedStore()
	pump := control.NewFeedPump(t.Context(), store)
	sink := &recordingSink{}
	pump.SetSink(sink.deliver)

	sessionID := uuid.New()
	pump.Signal(sessionID, 1)

	require.Eventually(t, func() bool {
		return store.callCount(sessionID) >= 1
	}, feedPumpWait, time.Millisecond)

	assert.Never(t, func() bool {
		return store.callCount(sessionID) != 1
	}, feedPumpQuiet, time.Millisecond)
	assert.Equal(t, 0, sink.pageCount())
}
