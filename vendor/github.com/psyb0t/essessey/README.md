# essessey

[![Go Reference](https://pkg.go.dev/badge/github.com/psyb0t/essessey.svg)](https://pkg.go.dev/github.com/psyb0t/essessey)
[![CI](https://github.com/psyb0t/essessey/actions/workflows/pipeline.yml/badge.svg?branch=main)](https://github.com/psyb0t/essessey/actions/workflows/pipeline.yml)
[![coverage](https://raw.githubusercontent.com/psyb0t/essessey/badges/coverage.svg)](https://github.com/psyb0t/essessey/actions/workflows/pipeline.yml)
[![version](https://raw.githubusercontent.com/psyb0t/essessey/badges/version.svg)](https://github.com/psyb0t/essessey/tags)
[![license](https://raw.githubusercontent.com/psyb0t/essessey/badges/license.svg)](LICENSE)
[![imported by](https://raw.githubusercontent.com/psyb0t/essessey/badges/importers.svg)](https://github.com/psyb0t/essessey/blob/badges/importers.md)

Say the letters out loud. That's the name. S-S-E.

Getting a model's answer to whoever's waiting for it — token by token, block
by block, in the order it actually fucking happened.

Here's the hill this package is prepared to die on: **SSE is a format, not a
transport.** Everybody lists it next to WebSocket and NATS like they're three
flavors of the same damn thing. They're not. `event:` / `data:` / blank line
exists for exactly one reason — an HTTP response body is a pipe with no seams,
so something has to mark where one event stops and the next starts. Hand those
same events to NATS or a WebSocket and that framing is dead weight: those
already deliver discrete messages. So here SSE is one binding that adds
framing, the message-oriented ones don't, and all of them carry the same
`Event`. A browser `EventSource` and a NATS subscriber decode the identical
JSON PAYLOAD — the envelope around it is what differs per binding, and that
difference is spelled out below rather than glossed over. That's the whole
fucking point.

It doesn't talk to a model — that's [elelem](https://github.com/psyb0t/elelem)'s
job, and `elelemstream` is the seam between them. It doesn't own your HTTP
handler, doesn't pick your broker, and doesn't drag half of npm's Go equivalent
into your build just to prove it supports one: the NATS and WebSocket bindings
are written against the smallest interface each actually needs, so you hand
them the connection you already have and this package stays at zero transport
dependencies. Zero. Check the `go.mod` yourself.

What it DOES own is the boring shit nobody wants to write twice — which content
block index a tool result belongs to, when a thinking block has to close before
the answer starts, and gluing a stream back together at the other end.

```go
sink := essessey.NewInMemorySink() // or sse.NewWriterSink(w), nats.NewSink(conn, "turn"), ws.NewSink(conn)
pub := essessey.NewPublisher(ctx, sink)

if err := pub.SendStreamPreamble(msgID, streamID, model); err != nil {
	return err
}

streamer := essessey.NewTextStreamer(pub, 0)
for chunk := range delta {
	if err := streamer.Write(ctx, chunk); err != nil {
		return err
	}
}

if err := streamer.Close(ctx); err != nil {
	return err
}

return pub.SendStreamEpilogue(essessey.StopReasonEndTurn, outputTokens)
```

## Contents

- [Quick start](#quick-start)
- [Reading a stream back](#reading-a-stream-back)
- [Why one Event, many bindings](#why-one-event-many-bindings)
- [What each package does](#what-each-package-does)
- [Resuming a dropped stream](#resuming-a-dropped-stream)
- [Zero transport dependencies](#zero-transport-dependencies)
- [elelemstream](#elelemstream)
- [Layout](#layout)
- [Development](#development)
- [License](#license)

## Quick start

```bash
go get github.com/psyb0t/essessey
```

A `Publisher` writes to any `Sink`. `InMemorySink` needs nothing at all to try
— it just collects whatever got emitted:

```go
ctx := context.Background()

sink := essessey.NewInMemorySink()
pub := essessey.NewPublisher(ctx, sink)

if err := pub.SendStreamPreamble("msg_1", "conv_1", "some-model"); err != nil {
	panic(err)
}

streamer := essessey.NewTextStreamer(pub, 0)

if err := streamer.Write(ctx, "Hello, "); err != nil {
	panic(err)
}

if err := streamer.Write(ctx, "world!"); err != nil {
	panic(err)
}

if err := streamer.Close(ctx); err != nil {
	panic(err)
}

if err := pub.SendStreamEpilogue(essessey.StopReasonEndTurn, 0); err != nil {
	panic(err)
}

fmt.Println(sink.Len(), "events emitted")
```

Swap `InMemorySink` for `sse.NewWriterSink(w)` (or `sse.NewHTTPSink(w)` behind
a flushing `http.ResponseWriter`), `nats.NewSink(conn, subjectPrefix)`, or
`ws.NewSink(conn)` and every line above the sink construction stays exactly the
fucking same — the `Publisher`, the streamer, and the event sequence have no
idea which delivery is on the other end, and no reason to.

## Reading a stream back

The other half. `Reassemble` drains any `Source` and hands back the finished
turn — you do not walk events yourself unless you want to:

```go
src := sse.NewSource(resp.Body) // or nats/ws Source, or SliceSource in a test

parsed := essessey.Reassemble(ctx, src)

fmt.Println(parsed.Text)       // every text delta, concatenated
fmt.Println(parsed.ToolNames)  // tools the model called
fmt.Println(parsed.Timeline)   // text and tool activity, in the order it happened
```

`ParsedStream` also carries `Tools` (each call matched to its result by content
block index — the bookkeeping this package exists to do for you), `Executions`,
`StreamID`, and `Error` if the stream carried one.

If you do want the raw events, a `Source` is just an iterator:

```go
for {
	ev, err := src.Next(ctx)
	if errors.Is(err, essessey.ErrNoMoreEvents) {
		break
	}
	if err != nil {
		return err
	}

	fmt.Println(ev.Event, string(ev.Data))
}
```

`ErrNoMoreEvents` ends the stream cleanly — it is the terminator, not a failure,
so a caller can range over a `Source` without special-casing it.

## Why one Event, many bindings

```go
type Event struct {
	ID    string          `json:"id,omitempty"`
	Event EventType       `json:"event"`
	Data  json.RawMessage `json:"data"`
}
```

That's the whole wire model: an optional id, a name, and a JSON payload.
`Sink` delivers it (`Emit(ctx, Event) error`); `Source` reads it back
(`Next(ctx) (Event, error)`, ending the stream with `ErrNoMoreEvents`).
Neither interface knows what "framing" even means — that's a property of the
binding underneath, not of the event.

`ID` is what makes a dropped connection recoverable. A browser `EventSource`
remembers the last id it saw and sends it back as `Last-Event-ID` on reconnect,
so a server can resume instead of restarting the stream — and a subscriber that
tracks ids can tell it MISSED one rather than silently rendering a gap. It is
`omitempty` deliberately: an empty `id:` field on the wire does not mean "no
id", it RESETS the receiver's resume point, so emitting one for an event that
simply has no id would throw away the position every earlier event established.

| binding | needs framing? | why |
|---|---|---|
| SSE (`io.Writer`, `http.ResponseWriter`) | yes | an HTTP response body is an undelimited byte stream; something has to mark where one event ends |
| NATS | no | every publish is already a discrete message |
| WebSocket | no | every write is already a discrete frame |

So the SSE binding owns a codec the other two never need, and it implements the
format as specified rather than the subset one consumer happens to use: one
`data:` field per line of the payload, `id:` and `retry:`, comment lines,
CRLF/LF/lone-CR terminators, the optional space after a field's colon, a
stripped byte order mark, and the rule that an event with no data field is
discarded rather than delivered empty. `FrameComment` writes the keep-alive
that stops an intermediary dropping an idle connection; `FrameRetry` tells the
client how long to wait before reconnecting.

The per-line `data:` split is not a detail. The format has no escaping and no
length prefix, so a newline inside a payload ends the FIELD and a blank line
ends the EVENT — emitting a multi-line payload as one `data:` line puts
different bytes on the wire than the caller passed, and for a payload
containing a blank line it forges an extra event out of the remainder.

What travels as `Data` is the same `json.RawMessage` either way, but be precise
about what "identical" means per binding: WebSocket writes the whole `Event` as
one JSON object, so a client gets `id`/`event`/`data` inline. NATS publishes the
payload RAW with the event type in the SUBJECT, so a subscriber reconstructs the
envelope from the subject it matched and does not see the id at all. SSE carries
all three as wire fields. The PAYLOAD is identical everywhere; the envelope
takes a different route on each binding, and a NATS subscriber has the most work
to do.

## What each package does

| Package | Responsibility |
|---|---|
| **Core** (this package) | `Event`, the `Sink`/`Source` interfaces, `Publisher` (one `Send*` method per protocol event, plus `SendStreamPreamble`/`SendStreamEpilogue` for the open/close pair), `TextStreamer`/`LineStreamer` for turning a chunk-at-a-time answer into correctly-indexed content blocks, and `Reassemble`, which drains a `Source` back into a `ParsedStream` — accumulated text, tool calls matched to their results by content-block index, and an ordered timeline of both. |
| **[sse](sse/README.md)** | The SSE format itself: `FrameLines` renders the wire bytes, `WriterSink`/`HTTPSink` write framed events to an `io.Writer` or a flushing `http.ResponseWriter`, and `Source` scans them back off an `io.Reader` — a malformed frame gets warn-logged and skipped instead of nuking the whole stream. |
| **[nats](nats/README.md)** | A `Sink` that publishes `Event.Data` unframed to `subjectPrefix.<eventType>`, and a `Source` whose `Deliver` method you wire in as a subscription callback. |
| **[ws](ws/README.md)** | A `Sink` that writes the whole `Event` as one `WriteJSON` call, and a `Source` whose `Deliver` method you wire into a read loop. |
| **[elelemstream](elelemstream/)** | Bridges [elelem](https://github.com/psyb0t/elelem)'s callbacks to this protocol — see below. |
| Retention (`store.go`, `multisink.go`) | `MultiSink` fans one `Emit` out to several sinks, and `EventStore` retains recent events per stream so a reconnecting client can be resumed — with `InMemoryEventStore` as a bounded, per-stream default. See [Resuming a dropped stream](#resuming-a-dropped-stream). |
| Test doubles (`memory.go`) | `InMemorySink` collects events instead of delivering them (not test-only — it's also what you want when a turn has to be fully produced before any of it gets released), and `SliceSource` replays a fixed slice, so feeding one `InMemorySink`'s `Events()` into a `SliceSource` round-trips a whole stream with no transport involved whatsoever. |

## Resuming a dropped stream

`Event.ID` gives a client a resume point; `EventStore` is what lets a server
honour it. The protocol gives you the mechanism, not the retention — you can
only resume to an event you still have.

Capture composes rather than wrapping. `MultiSink` fans one `Emit` to several
sinks, and `store.SinkFor(id)` is the store's write half, so retention is just
another destination:

```go
store, err := essessey.NewInMemoryEventStore(256) // per stream, oldest evicted
// A capacity of zero or less returns ErrInvalidCapacity rather than a store
// that accepts every append and resumes nothing.
live := essessey.NewMultiSink(httpSink, store.SinkFor(chatID))
```

Replay needs nothing new. Ask what came after the client's last id, hand it to a
`SliceSource`, and pump it through the ordinary sink — the same code path a live
stream uses, so the two cannot drift and the client cannot tell them apart:

```go
events, known, err := store.Since(ctx, chatID, lastEventID)
if !known {
	// Not retained: evicted, never seen, or from a previous process.
	// Restart the stream — do NOT guess.
}
```

That `known` flag is the whole design. When a resume point is missing, replaying
from the start duplicates everything the client already rendered and replaying
from now silently drops the events in between — the exact gap ids exist to
prevent. Only the caller knows which is acceptable, so the store refuses to
choose.

Four things worth knowing before wiring it up:

- **Replay through the wire sink alone, never the `MultiSink`** — otherwise each
  reconnect re-appends what it is replaying and the store grows without bound.
- **`streamID` is a security boundary.** Replay keyed only by event id would let
  a client presenting an id receive someone else's events. On reconnect the
  `streamID` arrives from the client, so authorize the resume exactly as you
  authorize opening the stream — `Since` only knows map keys, not owners.
- **Events without an id are still replayed**, they just cannot be resumed TO.
  Dropping them would silently skip real content.
- **An id-less frame still moves a client's resume point.** The format sets the
  last-event-id before it discards an event with no data, so a bare `id:` frame
  advances the client without delivering anything.

`InMemoryEventStore` is per-process and bounded — a reasonable default and a
reference implementation. Anything that must survive a restart or span replicas
wants its own `EventStore` against shared storage.

## Zero transport dependencies

`sse` needs nothing beyond the standard library. `nats` and `ws` each declare
the one single method they actually need from a client:

```go
// nats.Publisher
type Publisher interface {
	Publish(subject string, data []byte) error
}

// ws.Conn
type Conn interface {
	WriteJSON(v any) error
}
```

`*nats.Conn` (`github.com/nats-io/nats.go`) and a gorilla `*websocket.Conn`
already satisfy these as-is — you pass your own client in, and essessey never
imports either SDK. Which keeps your module graph clear of the `go mod vendor`
avalanche a real transport client drags along behind it, all for the sake of
one method per binding. One method. That's not worth a dependency.

## elelemstream

`elelemstream` is the one subpackage that imports
[elelem](https://github.com/psyb0t/elelem): it translates elelem's callback
stream (text deltas, reasoning deltas, tool-call starts and tool results) into
this package's block protocol, so an elelem-backed handler gets the same
`message_start` → content blocks → `message_stop` sequence without you
hand-rolling the translation. Everything else in this module — the core
package, `sse`, `nats`, `ws` — stays free of an elelem import; a caller who
isn't using elelem never pulls it in.

The block-index arithmetic it owns is the part worth reading before you go
poking at it — which index a tool result lands on, why parallel calls wreck the
naive implementation everyone writes first, and which invariants the tests pin
down. That lives next to the code, in
[elelemstream/README.md](elelemstream/README.md). Read it before you "simplify"
anything in there.

## Layout

```text
types.go, event.go             the wire types, EventType/Role/etc. constants, Event, Sink, Source
publisher.go                   Publisher and one Send* method per protocol event
streamer.go                    TextStreamer, LineStreamer
reassemble.go                  Source -> ParsedStream reconstruction
memory.go                      InMemorySink, SliceSource
multisink.go                   MultiSink — fan one Emit out to several Sinks
store.go                       EventStore, InMemoryEventStore — retention for resume
sse/                           the SSE format: codec, WriterSink, HTTPSink, Source
nats/                          NATS binding over a minimal Publisher interface
ws/                            WebSocket binding over a minimal Conn interface
elelemstream/                  elelem callbacks -> block protocol (imports elelem)
```

## Development

Every target runs inside the `Dockerfile.dev` image, so a clean host needs
Docker rather than a curated Go toolchain.

```bash
make dep           # tidy the module and re-vendor
make lint          # go fix, then golangci-lint at full strictness
make lint-fix      # the same, applying whatever it can fix itself
make test          # the suite, always with -race
make test-coverage # the suite plus the coverage floor CI enforces
make sec           # govulncheck + semgrep, merged into sec.sarif
make help          # the rest of it
```

## License

MIT. See [LICENSE](LICENSE).

See [CHANGELOG.md](CHANGELOG.md) for release notes.
