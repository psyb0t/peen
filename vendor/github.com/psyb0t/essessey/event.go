package essessey

import (
	"context"
	"encoding/json"
	"errors"
)

// Event is one protocol event on its way to a client: a name plus a JSON
// payload.
//
// This is the ONLY thing a binding has to carry, and it is deliberately free of
// any delivery detail. Data is json.RawMessage rather than string because it
// has always held JSON — the type now says so, and every non-byte-stream
// binding is spared a []byte conversion per event.
type Event struct {
	// ID is the event's identifier. Optional, and omitted from the wire when
	// empty.
	//
	// This is what makes a dropped connection recoverable. An SSE client
	// remembers the last ID it saw and sends it back as the Last-Event-ID
	// header when it reconnects, so a server can resume from that point
	// instead of restarting the stream. It is also the only way a subscriber
	// can notice it MISSED an event rather than silently rendering a gap.
	//
	// Empty is meaningful, which is why this is omitempty rather than always
	// emitted: per the SSE specification an EMPTY id field RESETS the client's
	// last-event-ID to the empty string, so writing `id:` for an event that
	// simply has no ID would destroy the resume point of the events before it.
	ID string `json:"id,omitempty"`

	Event EventType       `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// Sink delivers events to a client.
//
// SSE, NATS and WebSocket are NOT peers: SSE is a FORMAT whose framing exists
// because an HTTP body is an undelimited byte stream, while NATS and WebSocket
// are message-oriented and already have boundaries. So a byte-stream sink
// frames each event; a message-oriented sink carries Data as-is. Both satisfy
// this one method, and the payload a client deserializes is identical either
// way.
//
// Implementations live in subpackages — essessey/sse, essessey/nats,
// essessey/ws — and callers add their own by satisfying Emit.
//
// Emit must be safe for concurrent use: the tool loop can produce blocks from
// several goroutines.
type Sink interface {
	Emit(ctx context.Context, ev Event) error
}

// Source reads events back from a delivery, the mirror of Sink.
//
// This exists because the read side cannot assume a byte stream. An SSE source
// scans framed text out of an io.Reader, but NATS and WebSocket hand over
// DISCRETE events with no stream to scan — there is no io.Reader to pass. Both
// shapes satisfy Next, so reassembly (see Reassemble) works against any
// delivery rather than against SSE alone.
//
// Next returns ErrNoMoreEvents when the stream ends normally. Any other error
// is a real failure.
type Source interface {
	Next(ctx context.Context) (Event, error)
}

// ErrNoMoreEvents ends a Source's stream. Reassembly treats it as a clean end
// of input, not a failure, so a caller can range over a Source without
// special-casing the terminator.
var ErrNoMoreEvents = errors.New("no more events")
