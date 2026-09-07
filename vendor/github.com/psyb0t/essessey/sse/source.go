package sse

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/essessey"
)

// maxLineSize bounds a single scanned line (1 MiB) so a pathological upstream
// can't drive unbounded allocation.
const maxLineSize = 1024 * 1024

// utf8BOM is stripped once from the head of a stream. Decoding UTF-8 removes a
// leading byte order mark, and one left in place glues itself to the first
// field NAME — turning `event` into something that matches nothing and
// silently losing the first event of the stream.
const utf8BOM = "\ufeff"

// Source reads framed SSE bytes off an io.Reader and yields essessey.Event —
// the read-side mirror of WriterSink/HTTPSink.
//
// This implements the format's parsing algorithm rather than matching pairs of
// lines, because the two are not the same thing:
//
//   - An event ends at a BLANK LINE, not at whatever line comes next. A
//     payload spans as many `data:` fields as it has lines, and they are
//     joined back together with newlines.
//   - The space after a field's colon is OPTIONAL, so `data:x` and `data: x`
//     carry the same value. Requiring it makes conformant streams from other
//     producers unreadable.
//   - Lines end with CRLF, LF, or a lone CR.
//   - An event whose data buffer stayed empty is DISCARDED, not delivered.
//   - The last-event-ID persists across events until the producer changes it,
//     while the event type and data buffers reset after every dispatch.
type Source struct {
	scanner *bufio.Scanner

	dataBuf   strings.Builder
	eventType essessey.EventType

	lastEventID string
	retry       time.Duration

	bomChecked bool
}

// NewSource builds a Source scanning framed SSE bytes out of r.
func NewSource(r io.Reader) *Source {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, maxLineSize), maxLineSize)
	scanner.Split(scanFrameLines)

	return &Source{scanner: scanner}
}

// LastEventID returns the most recent id the producer sent, which persists
// across events until replaced.
//
// This is the value a client sends back as the Last-Event-ID request header on
// reconnect, so a producer can resume rather than restart the stream.
func (s *Source) LastEventID() string {
	return s.lastEventID
}

// Retry returns the reconnection delay the producer asked for, or zero if it
// never sent one.
func (s *Source) Retry() time.Duration {
	return s.retry
}

// Next returns the next event, or essessey.ErrNoMoreEvents once the underlying
// reader is exhausted cleanly.
//
// A partial event at end of input is discarded rather than delivered: the
// producer never terminated it, so its payload may be incomplete, and handing
// a caller truncated bytes is worse than admitting the stream ended.
func (s *Source) Next(ctx context.Context) (essessey.Event, error) {
	for s.scanner.Scan() {
		line := s.stripBOM(s.scanner.Text())

		if line == "" {
			ev, ok := s.dispatch()
			if !ok {
				continue
			}

			return ev, nil
		}

		if strings.HasPrefix(line, commentPrefix) {
			continue
		}

		name, value := parseFieldLine(line)
		s.consumeField(ctx, name, value)
	}

	if err := s.scanner.Err(); err != nil {
		return essessey.Event{}, ctxerrors.Wrap(err, "scan sse stream")
	}

	s.dropIncomplete(ctx)

	return essessey.Event{}, essessey.ErrNoMoreEvents
}

// stripBOM removes a byte order mark from the first line of the stream only.
func (s *Source) stripBOM(line string) string {
	if s.bomChecked {
		return line
	}

	s.bomChecked = true

	return strings.TrimPrefix(line, utf8BOM)
}

// consumeField applies one parsed field to the parser's buffers. An
// unrecognised name is ignored, which is what lets the format grow new fields
// without breaking existing readers.
func (s *Source) consumeField(ctx context.Context, name, value string) {
	switch name {
	case fieldNameEvent:
		s.eventType = value

	case fieldNameData:
		// Each data field contributes its value plus a newline; the final
		// newline is removed at dispatch. That is what rejoins a multi-line
		// payload into the bytes the producer started with.
		s.dataBuf.WriteString(value)
		s.dataBuf.WriteString(lineTerminator)

	case fieldNameID:
		s.consumeID(ctx, value)

	case fieldNameRetry:
		s.consumeRetry(ctx, value)
	}
}

// consumeID records a new last-event-ID, ignoring one that contains NUL.
//
// Ignoring leaves the PREVIOUS id intact rather than clearing it, so a
// malformed id cannot destroy a resume point that was already valid.
func (s *Source) consumeID(ctx context.Context, value string) {
	if strings.ContainsRune(value, 0) {
		ctxscope.GetLogger(ctx).Warn(
			"sse source: ignoring id containing NUL",
			"reason", "invalid_id",
		)

		return
	}

	s.lastEventID = value
}

// consumeRetry records a reconnection delay, ignoring anything that is not a
// plain run of ASCII digits.
func (s *Source) consumeRetry(ctx context.Context, value string) {
	logger := ctxscope.GetLogger(ctx)

	if !isASCIIDigits(value) {
		logger.Warn(
			"sse source: ignoring non-numeric retry",
			"reason", "invalid_retry",
		)

		return
	}

	ms, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		// All digits, but too many to fit an int64. Ignoring matches how every
		// other unusable value here is treated.
		logger.Warn(
			"sse source: ignoring out-of-range retry",
			"reason", "retry_overflow",
		)

		return
	}

	s.retry = time.Duration(ms) * time.Millisecond
}

// dispatch completes the buffered event at a blank line, reporting ok=false
// when there is nothing to deliver.
//
// The event type and data buffers reset here; the last-event-ID deliberately
// does NOT, because it describes the stream position rather than this event —
// a client reconnecting after an event that carried no id must still resume
// from the last id it did see.
func (s *Source) dispatch() (essessey.Event, bool) {
	data := s.dataBuf.String()
	eventType := s.eventType

	s.dataBuf.Reset()
	s.eventType = ""

	// No data field ever arrived, so there is no event to deliver — only
	// stream-level fields like a lone `retry:`, or a block of comments.
	if data == "" {
		return essessey.Event{}, false
	}

	return essessey.Event{
		ID:    s.lastEventID,
		Event: eventType,
		Data:  json.RawMessage(strings.TrimSuffix(data, lineTerminator)),
	}, true
}

// dropIncomplete warns about a partial event abandoned at end of input.
func (s *Source) dropIncomplete(ctx context.Context) {
	if s.dataBuf.Len() == 0 && s.eventType == "" {
		return
	}

	ctxscope.GetLogger(ctx).Warn(
		"sse source: dropping unterminated event at end of stream",
		"event", s.eventType,
		"reason", "incomplete_event",
	)

	s.dataBuf.Reset()
	s.eventType = ""
}

// scanFrameLines is a bufio.SplitFunc recognising all three line terminators
// the format allows: CRLF, a lone LF, and a lone CR.
//
// bufio.ScanLines handles the first two only. A producer using bare CR — which
// the format permits — would arrive as one enormous line and nothing in the
// stream would parse.
func scanFrameLines(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}

	for i := range len(data) {
		if data[i] == lineFeed {
			return i + len(lineTerminator), data[:i], nil
		}

		if data[i] == carriageReturn {
			return scanAtCarriageReturn(data, i, atEOF)
		}
	}

	if atEOF {
		return len(data), data, nil
	}

	return 0, nil, nil
}

// scanAtCarriageReturn resolves a CR found at index i into either a CRLF split,
// a lone-CR split, or a request for more input.
//
// The request-for-more case is the subtle one: a CR sitting at the very end of
// the buffer is ambiguous, because the LF of a CRLF pair may simply not have
// been read yet. Splitting there would later surface the LF as its own empty
// line — which reads as an event terminator the producer never sent, silently
// cutting an event in half.
func scanAtCarriageReturn(
	data []byte,
	i int,
	atEOF bool,
) (int, []byte, error) {
	atBufferEnd := i+1 == len(data)

	if atBufferEnd && !atEOF {
		return 0, nil, nil
	}

	if !atBufferEnd && data[i+1] == lineFeed {
		return i + len(crlfTerminator), data[:i], nil
	}

	return i + len(crTerminator), data[:i], nil
}

// scanFrameLines must satisfy bufio.SplitFunc.
var _ bufio.SplitFunc = scanFrameLines
