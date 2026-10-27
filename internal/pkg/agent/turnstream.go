package agent

import (
	"context"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/essessey"
)

// streamDeltaFlushInterval is how long stream deltas may collect before they
// are written together. A model streams hundreds of deltas per reply, and one
// write each holds the stream to the storage's commit rate.
const streamDeltaFlushInterval = 100 * time.Millisecond

func isStreamDelta(event Event) bool {
	return event.Type == essessey.EventTypeContentBlockDelta
}

// holdStreamDelta keeps the event to be written with the events after it,
// and reports whether it did. Only stream deltas wait, only on a durable turn,
// and only until the interval since the last write passes. The next record
// writes and publishes them, and every block ends with a stop event, so a
// held delta never outlives its stream.
func (t *runtimeTurn) holdStreamDelta(event Event) bool {
	t.sinkMutex.Lock()
	defer t.sinkMutex.Unlock()

	t.mutex.Lock()
	defer t.mutex.Unlock()

	if t.store == nil || !isStreamDelta(event) ||
		time.Since(t.lastFlushAt) >= streamDeltaFlushInterval {
		return false
	}

	t.events = append(t.events, event)
	t.unpublished = append(t.unpublished, event)

	return true
}

// publishHeldDeltas writes and publishes held deltas, so a sink refusing one
// stops the stream before a later event is recorded.
func (t *runtimeTurn) publishHeldDeltas(ctx context.Context) error {
	t.sinkMutex.Lock()

	t.mutex.Lock()
	hasHeld := len(t.unpublished) > 0
	t.mutex.Unlock()

	if !hasHeld {
		t.sinkMutex.Unlock()

		return nil
	}

	err := t.flushUnpublished(ctx)
	t.sinkMutex.Unlock()

	if err != nil {
		return ctxerrors.Wrap(err, "persist held stream deltas")
	}

	return t.drainOutbox()
}

// flushUnpublished makes every recorded event durable and moves it to the
// outbox in record order. The caller holds sinkMutex, so batches enter the
// outbox in the order they were written.
func (t *runtimeTurn) flushUnpublished(ctx context.Context) error {
	if err := t.checkpoint(context.WithoutCancel(ctx)); err != nil {
		return err
	}

	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.outbox = append(t.outbox, t.unpublished...)
	t.unpublished = nil
	t.lastFlushAt = time.Now()

	return nil
}

// drainOutbox hands durable events to the observer and sink without holding
// a lock, because a sink may record or checkpoint itself. One goroutine
// drains at a time; events that arrive meanwhile join its loop, which keeps
// their order. A sink error stops the stream: the events after the refused
// one stay stored but are not published.
func (t *runtimeTurn) drainOutbox() error {
	t.mutex.Lock()
	if t.isDraining {
		t.mutex.Unlock()

		return nil
	}

	t.isDraining = true

	var refusal error

	for len(t.outbox) > 0 && refusal == nil {
		events := t.outbox
		t.outbox = nil
		sink := t.sink
		t.mutex.Unlock()

		refusal = t.publishEvents(sink, events)

		t.mutex.Lock()
	}

	if refusal != nil {
		t.outbox = nil
	}

	t.isDraining = false
	t.mutex.Unlock()

	return refusal
}

func (t *runtimeTurn) publishEvents(sink EventSink, events []Event) error {
	for _, event := range events {
		if err := t.publishEvent(sink, event); err != nil {
			return err
		}
	}

	return nil
}

func (t *runtimeTurn) publishEvent(sink EventSink, event Event) error {
	if t.observer != nil {
		t.observer(event)
	}

	if sink == nil {
		return nil
	}

	return sink(event)
}
