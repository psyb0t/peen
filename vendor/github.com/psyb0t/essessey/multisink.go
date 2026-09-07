package essessey

import (
	"context"
	"errors"

	"github.com/psyb0t/ctxerrors"
)

// MultiSink fans one Emit out to several Sinks.
//
// It exists because every other Sink is terminal — there was no way to send the
// same event to two places. The motivating case is retention: a stream that
// writes to the client AND to an EventStore, so a reconnecting client can be
// resumed. It covers the other obvious ones too (a wire sink plus an audit log,
// or SSE plus NATS) without any of them needing a bespoke wrapper.
//
// Emit is safe for concurrent use if the wrapped Sinks are; MultiSink holds no
// mutable state of its own.
type MultiSink struct {
	sinks []Sink
}

// NewMultiSink returns a Sink that forwards to every sink given, in order.
//
// Zero sinks is allowed and discards everything. That is deliberate: it lets a
// caller build the list conditionally without special-casing empty.
func NewMultiSink(sinks ...Sink) *MultiSink {
	return &MultiSink{sinks: sinks}
}

// Emit forwards ev to every sink and returns the joined error of any that
// failed.
//
// It does NOT stop at the first failure. A full store or a broken audit sink
// must not abort delivery to the client — the user's stream is the thing that
// matters and the rest are copies. Equally the failure is not swallowed: every
// error comes back joined, so a caller that cares can inspect it with
// errors.Is and one that does not still sees a non-nil error rather than
// silence.
//
// The consequence worth knowing: a non-nil error does NOT mean the event went
// nowhere. It means at least one destination missed it.
func (s *MultiSink) Emit(ctx context.Context, ev Event) error {
	var errs []error

	for _, sink := range s.sinks {
		if err := sink.Emit(ctx, ev); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) == 0 {
		return nil
	}

	return ctxerrors.Wrap(errors.Join(errs...), "emit to sinks")
}
