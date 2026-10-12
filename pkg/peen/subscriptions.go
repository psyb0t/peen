package peen

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/agent"
)

const defaultSubscriptionBuffer = 32

// Subscription receives live events across root, wake and child turns.
// Slow readers lose events; Dropped reports that loss so callers can replay
// durable records. Close releases the subscription without cancelling work.
type Subscription struct {
	Events  <-chan Event
	channel chan Event
	dropped atomic.Uint64
	stop    func()
}

func (s *Subscription) Close()          { s.stop() }
func (s *Subscription) Dropped() uint64 { return s.dropped.Load() }

type eventHub struct {
	mutex       sync.Mutex
	buffer      int
	closed      bool
	subscribers map[uuid.UUID]map[*Subscription]struct{}
}

func newEventHub(buffer int) *eventHub {
	if buffer == 0 {
		buffer = defaultSubscriptionBuffer
	}

	return &eventHub{
		buffer:      buffer,
		subscribers: map[uuid.UUID]map[*Subscription]struct{}{},
	}
}

// Subscribe validates the session and receives subsequent durable events.
// The caller owns closing the subscription; it does not keep Runtime.Close
// waiting for a consumer that stopped reading.
func (s *Session) Subscribe(ctx context.Context) (*Subscription, error) {
	ctx, finish, err := s.begin(ctx)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "subscribe to embedded session")
	}
	defer finish()

	if _, err := s.runtime.store.Get(ctx, s.id); err != nil {
		return nil, ctxerrors.Wrap(err, "subscribe to embedded session")
	}

	hub := s.runtime.eventHub
	hub.mutex.Lock()
	defer hub.mutex.Unlock()

	sub := &Subscription{channel: make(chan Event, hub.buffer)}
	sub.Events = sub.channel

	if hub.subscribers[s.id] == nil {
		hub.subscribers[s.id] = map[*Subscription]struct{}{}
	}

	hub.subscribers[s.id][sub] = struct{}{}

	var once sync.Once

	sub.stop = func() {
		once.Do(func() {
			hub.mutex.Lock()
			defer hub.mutex.Unlock()

			if _, exists := hub.subscribers[s.id][sub]; exists {
				delete(hub.subscribers[s.id], sub)
				close(sub.channel)
			}
		})
	}

	return sub, nil
}

func (h *eventHub) publish(id uuid.UUID, event agent.Event) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	for sub := range h.subscribers[id] {
		value := Event{
			SessionID: id.String(),
			RequestID: event.RequestID.String(), Type: event.Type,
			Payload: slices.Clone(event.Payload),
		}
		select {
		case sub.channel <- value:
		default:
			sub.dropped.Add(1)
		}
	}
}

func (h *eventHub) close() {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	if h.closed {
		return
	}

	h.closed = true
	for _, subscribers := range h.subscribers {
		for sub := range subscribers {
			close(sub.channel)
		}
	}

	clear(h.subscribers)
}
