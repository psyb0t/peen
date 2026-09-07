package sse

import "errors"

// ErrNotAFlusher means the http.ResponseWriter cannot flush.
//
// Refused at construction rather than tolerated: a non-flushing writer buffers
// the whole response, so the stream would arrive as one lump at the end. That
// looks like a slow model rather than a wiring bug, which is exactly the kind
// of failure that survives to production.
var ErrNotAFlusher = errors.New("response writer is not an http.Flusher")
