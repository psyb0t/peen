package essessey

import (
	"context"
	"errors"
	"sync"

	"github.com/psyb0t/ctxerrors"
)

// ErrInvalidCapacity rejects a non-positive retention size at construction.
//
// A zero-capacity store would accept every Append and answer every Since with
// "not retained" — a resume buffer that silently never resumes. Failing at
// construction turns that into a startup error rather than a mystery later.
var ErrInvalidCapacity = errors.New("capacity must be positive")

// EventStore retains recent events per stream so a client that reconnects can
// resume from where it left off.
//
// streamID is the same identifier Publisher.SendMessageStart puts on the wire
// as MessageMeta.StreamID — whatever the caller's domain calls one stream (a
// conversation, a job, a document build). essessey never mints it; it is an
// opaque key. Nothing forces the two to match, and keying retention at a
// finer grain (per turn, per connection) is a legitimate choice that trades a
// smaller buffer for losing replay of earlier turns. Using the id the client
// already learned from message_start is the common case.
//
// It is also untrusted on the way back in: a reconnecting client supplies the
// streamID, and Since resolves it as a map key without any notion of who owns
// it. Authorize the resume exactly as you authorize opening the stream.
//
// The read side deliberately reports whether the resume point is KNOWN instead
// of guessing. If an id has aged out, or never existed, neither available
// answer is safe: replaying from the start duplicates everything the client
// already rendered, and replaying from now silently drops the events in
// between — the exact gap event ids exist to prevent. Only the caller knows
// whether to restart the stream or fail the request, so Since hands that
// decision back.
type EventStore interface {
	// Append records ev as the most recent event of streamID.
	Append(ctx context.Context, streamID string, ev Event) error

	// Since returns the events recorded AFTER lastEventID, oldest first.
	//
	// known=false means the id is not in retention — evicted, never seen, or
	// from a previous process — and the caller must decide what to do rather
	// than receive a silently wrong slice. known=true with an empty slice means
	// the client is already up to date.
	Since(
		ctx context.Context,
		streamID string,
		lastEventID string,
	) ([]Event, bool, error)

	// Clear drops everything retained for streamID.
	Clear(ctx context.Context, streamID string) error
}

// InMemoryEventStore is a bounded, per-stream, in-process EventStore.
//
// Per stream it keeps a ring buffer for order and O(1) eviction, plus an index
// from event id to sequence number for O(1) resume lookup. The index is what a
// ring alone cannot provide: event ids are opaque strings with no ordering, so
// without it every resume would be a linear scan.
//
// Two behaviours are worth knowing before relying on it:
//
//   - Events with NO id are still retained and still replayed. They cannot be
//     resumed TO — nothing can name them — but they must come back if they fall
//     after the resume point, or the resume would silently skip them.
//   - Ids are assumed unique per stream. Reusing one moves the resume point to
//     the later occurrence, so resuming from it skips everything in between.
//
// Safe for concurrent use. Compose it for capture via SinkFor.
type InMemoryEventStore struct {
	capacity int

	mu      sync.RWMutex
	streams map[string]*streamBuffer
}

// storedEvent pairs an event with the monotonic sequence it was appended at.
// The sequence, not the id, establishes "after" — ids are opaque and may be
// absent entirely.
type storedEvent struct {
	seq int64
	ev  Event
}

type streamBuffer struct {
	ring  []storedEvent
	head  int   // next slot to write
	count int   // valid entries, <= len(ring)
	next  int64 // sequence for the next append

	// index maps event id to the sequence it was appended at. Only ids that
	// were actually present appear here.
	index map[string]int64
}

// NewInMemoryEventStore returns a store retaining up to capacity events per
// stream, evicting the oldest first.
func NewInMemoryEventStore(capacity int) (*InMemoryEventStore, error) {
	if capacity <= 0 {
		return nil, ctxerrors.Wrapf(
			ErrInvalidCapacity, "new in-memory event store: got %d", capacity,
		)
	}

	return &InMemoryEventStore{
		capacity: capacity,
		streams:  make(map[string]*streamBuffer),
	}, nil
}

// Append records ev as the most recent event of streamID, evicting the oldest
// once the stream is at capacity.
func (s *InMemoryEventStore) Append(
	_ context.Context,
	streamID string,
	ev Event,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	buf, ok := s.streams[streamID]
	if !ok {
		buf = &streamBuffer{
			ring:  make([]storedEvent, s.capacity),
			index: make(map[string]int64),
		}
		s.streams[streamID] = buf
	}

	buf.append(ev)

	return nil
}

// Since returns the events of streamID recorded after lastEventID.
func (s *InMemoryEventStore) Since(
	_ context.Context,
	streamID string,
	lastEventID string,
) ([]Event, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	buf, ok := s.streams[streamID]
	if !ok {
		return nil, false, nil
	}

	events, known := buf.since(lastEventID)

	return events, known, nil
}

// Clear drops everything retained for streamID.
func (s *InMemoryEventStore) Clear(_ context.Context, streamID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.streams, streamID)

	return nil
}

// SinkFor returns a Sink that appends everything it receives to streamID.
//
// This is the capture half, and it exists so retention composes with MultiSink
// rather than needing a wrapper type:
//
//	live := essessey.NewMultiSink(httpSink, store.SinkFor(chatID))
//
// Do NOT replay INTO that MultiSink — replay through the wire sink alone, or
// every reconnect re-appends what it is replaying and the store grows without
// bound.
//
// It returns the Sink interface deliberately: this value exists to be composed
// alongside other Sinks, and a concrete unexported type would force callers to
// name something they cannot reference.
//
//nolint:ireturn // see the paragraph above
func (s *InMemoryEventStore) SinkFor(streamID string) Sink {
	return &storeSink{store: s, streamID: streamID}
}

type storeSink struct {
	store    *InMemoryEventStore
	streamID string
}

func (s *storeSink) Emit(ctx context.Context, ev Event) error {
	return s.store.Append(ctx, s.streamID, ev)
}

// append writes ev at the head, evicting whatever occupied the slot.
func (b *streamBuffer) append(ev Event) {
	seq := b.next
	b.next++

	if b.count == len(b.ring) {
		b.evict(b.ring[b.head])
	}

	b.ring[b.head] = storedEvent{seq: seq, ev: ev}
	b.head = (b.head + 1) % len(b.ring)

	if b.count < len(b.ring) {
		b.count++
	}

	if ev.ID != "" {
		b.index[ev.ID] = seq
	}
}

// evict removes an outgoing entry's id from the index.
//
// The sequence check matters: if the same id was appended twice, the index
// already points at the NEWER sequence, and deleting on the older one's way out
// would destroy a resume point that is still live.
func (b *streamBuffer) evict(outgoing storedEvent) {
	if outgoing.ev.ID == "" {
		return
	}

	indexed, ok := b.index[outgoing.ev.ID]
	if ok && indexed == outgoing.seq {
		delete(b.index, outgoing.ev.ID)
	}
}

// since returns everything appended after lastEventID, and whether that id was
// a known resume point at all.
func (b *streamBuffer) since(lastEventID string) ([]Event, bool) {
	seq, ok := b.index[lastEventID]
	if !ok {
		return nil, false
	}

	events := make([]Event, 0, b.count)
	oldest := (b.head - b.count + len(b.ring)) % len(b.ring)

	for i := range b.count {
		stored := b.ring[(oldest+i)%len(b.ring)]
		if stored.seq <= seq {
			continue
		}

		events = append(events, stored.ev)
	}

	return events, true
}
