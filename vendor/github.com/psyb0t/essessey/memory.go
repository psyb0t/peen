package essessey

import (
	"context"
	"sync"
)

// InMemorySink collects emitted events instead of delivering them.
//
// Not test-only: it is also the buffer you want when a turn must be fully
// produced before any of it is released (a durable replay, a moderation pass).
// It lives in the core package rather than a test helper package for that
// reason, and because every binding's tests need it.
type InMemorySink struct {
	mu     sync.Mutex
	events []Event
}

// NewInMemorySink builds an empty InMemorySink.
func NewInMemorySink() *InMemorySink {
	return &InMemorySink{}
}

// Emit appends the event.
func (s *InMemorySink) Emit(_ context.Context, ev Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.events = append(s.events, ev)

	return nil
}

// Events returns a COPY of what was emitted, so a caller ranging over the
// result cannot race a concurrent Emit or mutate the sink's own slice.
func (s *InMemorySink) Events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]Event(nil), s.events...)
}

// Len reports how many events have been emitted.
func (s *InMemorySink) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.events)
}

// SliceSource replays a fixed slice of events, the Source counterpart to
// InMemorySink. Feeding an InMemorySink's Events into one round-trips a stream
// with no transport involved — which is how every binding proves it preserves
// the event sequence.
type SliceSource struct {
	mu     sync.Mutex
	events []Event
	next   int
}

// NewSliceSource builds a SliceSource over a copy of events, so a later
// mutation by the caller cannot rewrite a replay already in progress.
func NewSliceSource(events []Event) *SliceSource {
	return &SliceSource{events: append([]Event(nil), events...)}
}

// Next returns the next event, or ErrNoMoreEvents once the slice is exhausted.
func (s *SliceSource) Next(_ context.Context) (Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.next >= len(s.events) {
		return Event{}, ErrNoMoreEvents
	}

	ev := s.events[s.next]
	s.next++

	return ev, nil
}
