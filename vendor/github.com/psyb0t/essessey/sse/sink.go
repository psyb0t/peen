package sse

import (
	"context"
	"io"
	"net/http"
	"sync"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/essessey"
)

// WriterSink frames and writes each event to an underlying io.Writer,
// guarding concurrent writes with a mutex — the tool loop can produce blocks
// from several goroutines.
type WriterSink struct {
	mu sync.Mutex
	w  io.Writer
}

// NewWriterSink builds a WriterSink over w.
func NewWriterSink(w io.Writer) *WriterSink {
	return &WriterSink{w: w}
}

// Emit writes the framed event to the underlying writer.
func (s *WriterSink) Emit(_ context.Context, ev essessey.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := io.WriteString(s.w, FrameLines(ev)); err != nil {
		return ctxerrors.Wrap(err, "write sse frame")
	}

	return nil
}

// HTTPSink frames and writes each event straight to a flushing
// http.ResponseWriter, flushing after every event so the client sees it
// immediately instead of buffered behind the transport.
type HTTPSink struct {
	mu sync.Mutex
	w  http.ResponseWriter
	f  http.Flusher
}

// NewHTTPSink builds an HTTPSink over w. It errors if w does not implement
// http.Flusher — a non-flushing writer would buffer the whole stream and
// defeat streaming.
func NewHTTPSink(w http.ResponseWriter) (*HTTPSink, error) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, ctxerrors.Wrap(ErrNotAFlusher, "new http sink")
	}

	return &HTTPSink{w: w, f: f}, nil
}

// Emit writes the framed event and flushes it to the client immediately.
func (s *HTTPSink) Emit(_ context.Context, ev essessey.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := io.WriteString(s.w, FrameLines(ev)); err != nil {
		return ctxerrors.Wrap(err, "write sse frame")
	}

	s.f.Flush()

	return nil
}
