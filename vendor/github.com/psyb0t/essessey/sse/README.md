# sse — the SSE wire format

This package is the only place in essessey that knows what bytes look like. It
frames `essessey.Event` values into `text/event-stream` and parses them back.

Everything here implements the format defined by the WHATWG HTML Living
Standard under "Server-sent events", including the parts a single-consumer
implementation is tempted to skip.

## Contents

- [Why only this package frames](#why-only-this-package-frames)
- [Writing a stream](#writing-a-stream)
- [The HTTP headers you must set](#the-http-headers-you-must-set)
- [What the bytes look like](#what-the-bytes-look-like)
- [Reading a stream](#reading-a-stream)
- [Keep-alives and reconnection](#keep-alives-and-reconnection)
- [Resuming](#resuming)
- [Things that will bite you](#things-that-will-bite-you)

## Why only this package frames

An HTTP response body is one undelimited byte stream. Nothing in it says where
one event stops and the next begins, so the format invents a delimiter: a blank
line. NATS and WebSocket already deliver discrete messages, so they need none of
this — which is why `nats` and `ws` carry `Event.Data` as-is and only `sse` owns
a codec.

## Writing a stream

Two sinks, same framing. `WriterSink` targets any `io.Writer`; `HTTPSink`
targets a flushing `http.ResponseWriter` and flushes after every event so the
client sees it immediately instead of at the end.

```go
func handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")

	sink, err := sse.NewHTTPSink(w)
	if err != nil {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)

		return
	}

	pub := essessey.NewPublisher(r.Context(), sink)

	if err := pub.SendStreamPreamble("msg_1", "conv_1", "some-model"); err != nil {
		return
	}

	streamer := essessey.NewTextStreamer(pub, 0)
	for _, chunk := range []string{"Hello, ", "world!"} {
		if err := streamer.Write(r.Context(), chunk); err != nil {
			return
		}
	}

	_ = streamer.Close(r.Context())
	_ = pub.SendStreamEpilogue(essessey.StopReasonEndTurn, 0)
}
```

`NewHTTPSink` returns an error when the writer is not an `http.Flusher`. That is
deliberate: a non-flushing writer buffers the entire response, so the stream
arrives as one lump at the end — which looks like a slow model rather than a
wiring bug, and would otherwise survive to production.

To write somewhere that isn't HTTP — a file, a buffer, a pipe — use
`NewWriterSink(w)`. It takes the same events and produces the same bytes.

## The HTTP headers you must set

**This package does not set them, and your stream will not work without them.**

`Content-Type: text/event-stream` is not advisory: a browser `EventSource` fails
the connection outright if the response carries anything else. `Cache-Control:
no-store` stops an intermediary buffering or replaying the stream.

They are the handler's job because the handler owns the response, and essessey
does not own your routes. Set them before you construct the sink — after the
first `Emit` the headers are already on the wire.

## What the bytes look like

`FrameLines` renders one event. This is the whole wire format:

```go
sse.FrameLines(essessey.Event{
	ID:    "42",
	Event: "content_block_delta",
	Data:  json.RawMessage(`{"text":"hi"}`),
})
```

```
id: 42
event: content_block_delta
data: {"text":"hi"}

```

The trailing blank line is the event terminator — that is what tells a receiver
the event is complete.

**A multi-line payload becomes one `data:` field per line.** The format has no
escaping and no length prefix, so a raw newline inside a value would end the
field, and a raw blank line would end the *event*:

```go
sse.FrameLines(essessey.Event{
	Event: "chunk",
	Data:  json.RawMessage("{\n  \"a\": 1\n}"),
})
```

```
event: chunk
data: {
data:   "a": 1
data: }

```

The receiver rejoins them with newlines, so the payload arrives byte-identical.
Emitting that as a single `data:` line would put different bytes on the wire
than you passed — and for a payload containing a blank line, it would forge an
extra event out of the remainder.

Fields that cannot survive one line (CR, LF, NUL) are stripped from `ID` and
`Event` before framing, so a caller-supplied event type can never inject extra
events into the stream.

## Reading a stream

`Source` parses framed bytes back into events. It works against anything that
reads — an HTTP response body, a file, a `strings.Reader` in a test:

```go
src := sse.NewSource(resp.Body)

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

Or hand the whole `Source` to `essessey.Reassemble` and get the finished turn —
accumulated text, tool calls matched to their results, and an ordered timeline —
instead of handling events yourself.

The parser accepts what the format allows, not just what this package writes:
the space after a field's colon is optional, so `data:x` and `data: x` are the
same value; lines end with CRLF, LF **or** a lone CR; a leading byte order mark
is stripped; comment lines are skipped; unknown fields are ignored.

## Keep-alives and reconnection

```go
io.WriteString(w, sse.FrameComment("keep-alive"))  // ": keep-alive\n"
io.WriteString(w, sse.FrameRetry(5*time.Second))   // "retry: 5000\n\n"
```

A comment is a line the receiver ignores. Its use is keeping an idle connection
alive — an intermediary that sees no bytes for long enough will drop the
connection, and a comment is the cheapest traffic that prevents it. Send one
every 15 seconds or so on a stream that can go quiet.

`retry` tells the client how long to wait before reconnecting. It describes the
stream rather than any one event, which is why it is its own frame instead of a
field on `Event`.

## Resuming

`Source` tracks the stream-level state a reconnecting client needs:

```go
src.LastEventID() // the last id seen; persists across events that carry none
src.Retry()       // the reconnection delay the server asked for, or 0
```

`LastEventID` is what a client sends back as the `Last-Event-ID` request header
so a server can resume instead of restarting. See the root README for the server
half — `essessey.EventStore` and the retention buffer.

## Things that will bite you

- **No `Content-Type: text/event-stream` means no stream.** See above. This is
  the most common wiring failure and nothing in this package can catch it for
  you.
- **An empty `id:` is not "no id" — it RESETS the client's resume point.** That
  is why `Event.ID` is `omitempty`: emitting an empty one would throw away the
  position every earlier event established.
- **A frame carrying an `id:` but no `data:` still advances the client's resume
  point.** The format sets the last-event-id *before* it discards an event with
  no data, so a bare id frame moves the client without delivering anything. Do
  not use them as bookmarks.
- **An event with no `data:` field at all is discarded, not delivered empty.** A
  `data:` field with an *empty value* is different — that dispatches with an
  empty payload.
- **A payload's own newlines are handled for you, but only through
  `FrameLines`.** Writing `data:` lines by hand re-introduces every problem the
  codec exists to solve.
