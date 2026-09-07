// Package essessey streams an LLM turn to a client over whichever delivery the
// caller already has.
//
// The whole wire model is one Event: a name plus a JSON payload. A Sink
// delivers it, a Source reads it back, and neither knows what "framing" means —
// that belongs to the binding underneath.
//
// # SSE is a format, not a transport
//
// SSE gets listed alongside WebSocket and NATS as though the three were
// interchangeable. They are not. The event:/data:/blank-line framing exists
// because an HTTP response body is an undelimited byte stream, so something has
// to mark where one event ends. NATS and WebSocket already deliver discrete
// messages, so that framing would be dead weight there.
//
// Hence the split: the sse subpackage owns a codec, the nats and ws
// subpackages own none, and all three move the same Event. A browser
// EventSource and a NATS subscriber decode identical JSON.
//
// # What lives here
//
// Publisher turns protocol events into Sink emissions, with one Send method per
// event plus SendStreamPreamble/SendStreamEpilogue for the open/close pair.
// TextStreamer and LineStreamer accumulate a chunk-at-a-time answer into
// correctly-indexed content blocks. Reassemble drains a Source back into a
// ParsedStream — text, tool calls matched to their results, and an ordered
// timeline.
//
// Blocks open LAZILY, on first content, and the index advances only when a
// block that actually opened is closed. A round producing no text of a given
// kind must therefore emit nothing and burn no index — get that wrong and the
// client renders a blank card, or every later block shifts by one.
//
// # What lives elsewhere
//
// Nothing here talks to a model; that is elelem's job, and the elelemstream
// subpackage is the seam. It is the only subpackage importing elelem, so this
// package and the transport bindings stay free of it.
package essessey
