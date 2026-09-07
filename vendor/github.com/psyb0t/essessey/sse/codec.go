// Package sse is the SSE FORMAT binding for essessey: the wire codec plus the
// byte-stream Sinks (WriterSink, HTTPSink) and the byte-stream Source that
// frame/parse it.
//
// SSE is a FORMAT, not a transport peer of NATS/WebSocket. Its framing exists
// because an HTTP response body is an undelimited byte stream and something
// must mark where one event ends — message-oriented transports need none. So
// this package owns ALL framing; other bindings own none.
//
// The wire format implemented here is the one defined by the WHATWG HTML
// Living Standard under "Server-sent events", including the parts a
// single-consumer implementation is tempted to skip: multi-line payloads, the
// optional space after a field's colon, all three line terminators, comment
// lines, and the empty-data rule.
package sse

import (
	"strconv"
	"strings"
	"time"

	"github.com/psyb0t/essessey"
)

// SSE wire framing. The blank line terminates an event.
//
// These are FIELD NAMES and a bare colon, WITHOUT the conventional trailing
// space. The space after the colon is optional in the format — a conformant
// producer may omit it — so the parser must not require it. Writing it is
// conventional and kept, but that happens at frame time.
const (
	fieldNameEvent = "event"
	fieldNameData  = "data"
	fieldNameID    = "id"
	fieldNameRetry = "retry"

	fieldSeparator = ":"
	// fieldValuePad is the single optional space a producer conventionally
	// writes after the colon, and which a parser strips if present.
	fieldValuePad = " "

	// commentPrefix marks a line the receiver ignores. Its documented use is a
	// keep-alive: intermediaries drop idle connections, and a comment is
	// traffic that costs the receiver nothing to discard.
	commentPrefix = fieldSeparator

	lineTerminator  = "\n"
	frameTerminator = "\n"

	// The three line terminators the format recognises. Named so the scanner
	// can advance by their real lengths instead of by bare numbers.
	crlfTerminator = "\r\n"
	crTerminator   = "\r"

	lineFeed       = '\n'
	carriageReturn = '\r'
)

// FrameLines renders the canonical wire bytes for ev: the optional `id:` line,
// the `event:` line, one `data:` line PER LINE of the payload, then a blank
// line terminator.
//
// The per-line data split is the whole reason this is not a single Sprintf. A
// payload is arbitrary bytes; the format has no escaping and no length prefix,
// so a raw newline inside a value ends the FIELD and a raw blank line ends the
// EVENT. Emitting a multi-line payload as one `data:` line therefore puts
// something on the wire that differs from what the caller passed — and for a
// payload containing a blank line it forges an extra event out of the
// remainder.
func FrameLines(ev essessey.Event) string {
	var b strings.Builder

	// Written only when non-empty: an empty `id:` field is not a no-op, it
	// RESETS the receiver's last-event-ID and discards the resume point built
	// up by the events before it.
	if ev.ID != "" {
		writeField(&b, fieldNameID, sanitizeFieldValue(ev.ID))
	}

	if ev.Event != "" {
		writeField(&b, fieldNameEvent, sanitizeFieldValue(ev.Event))
	}

	// An empty payload still gets one empty data line. Dropping it would leave
	// an event whose data buffer never becomes non-empty, and a receiver
	// discards those without dispatching — the event would vanish rather than
	// arrive empty.
	for _, line := range splitPayloadLines(string(ev.Data)) {
		writeField(&b, fieldNameData, line)
	}

	b.WriteString(frameTerminator)

	return b.String()
}

// FrameComment renders a comment line, which a conformant receiver ignores.
//
// This is the format's keep-alive: an idle connection can be dropped by an
// intermediary that sees no bytes, and a comment is the cheapest traffic that
// prevents it without the receiver needing to understand anything.
func FrameComment(text string) string {
	return commentPrefix + fieldValuePad +
		sanitizeFieldValue(text) + lineTerminator
}

// FrameRetry renders a `retry:` line telling the receiver how long to wait
// before reconnecting after the stream drops.
//
// It describes the STREAM rather than any one event, which is why it is its
// own frame instead of a field on Event. The format carries milliseconds and a
// receiver ignores any value that is not all ASCII digits, so a negative
// duration is clamped to zero rather than written as something that would be
// silently discarded.
func FrameRetry(d time.Duration) string {
	ms := max(d.Milliseconds(), 0)

	return fieldNameRetry + fieldSeparator + fieldValuePad +
		strconv.FormatInt(ms, 10) + lineTerminator + frameTerminator
}

// writeField appends one `name: value` line.
func writeField(b *strings.Builder, name, value string) {
	b.WriteString(name)
	b.WriteString(fieldSeparator)
	b.WriteString(fieldValuePad)
	b.WriteString(value)
	b.WriteString(lineTerminator)
}

// splitPayloadLines splits a payload into the lines that become individual
// `data:` fields, treating CRLF, LF and a lone CR as terminators — the same
// three the parser recognises on the way back in.
//
// An empty payload yields one empty line so the caller still writes a single
// `data:` field; see FrameLines for why that matters.
func splitPayloadLines(payload string) []string {
	if payload == "" {
		return []string{""}
	}

	normalized := strings.ReplaceAll(payload, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")

	return strings.Split(normalized, lineTerminator)
}

// sanitizeFieldValue strips the characters that cannot survive a single-line
// field: CR and LF end the line early and let the remainder be read as further
// fields — or as an entire extra event — and NUL makes a receiver discard an
// `id` outright.
//
// Stripping rather than escaping is not a shortcut. The format defines no
// escape sequence, so there is nothing to escape TO. Stripping is also
// preferred to returning an error, because this function's contract is that
// whatever it returns can be written verbatim: a Sink that emitted a
// half-valid frame would corrupt every event after it, not just this one.
func sanitizeFieldValue(value string) string {
	if !strings.ContainsAny(value, "\r\n\x00") {
		return value
	}

	return strings.NewReplacer("\r", "", "\n", "", "\x00", "").Replace(value)
}

// parseFieldLine splits one non-comment line into field name and value, per
// the format's rule: everything before the first colon is the name, everything
// after is the value, and ONE leading space in the value is removed if present.
//
// A line with no colon at all is a field name with an empty value — not a
// malformed line to be skipped.
func parseFieldLine(line string) (string, string) {
	name, value, found := strings.Cut(line, fieldSeparator)
	if !found {
		return line, ""
	}

	// Exactly one space, and only if present. Trimming all leading whitespace
	// would corrupt a payload that legitimately begins with spaces.
	return name, strings.TrimPrefix(value, fieldValuePad)
}

// isASCIIDigits reports whether s is non-empty and all ASCII digits, the
// format's condition for accepting a `retry` value. Anything else is ignored
// rather than treated as an error.
func isASCIIDigits(s string) bool {
	if s == "" {
		return false
	}

	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}

	return true
}
