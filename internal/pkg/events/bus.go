package events

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/metrics"
)

const (
	defaultMaxPendingPerSession = 256
	defaultMaxSummaryBytes      = 4096
	defaultMaxDataBytes         = 64 * 1024
	defaultSubscriberBuffer     = 32

	reasonEventDataTooLarge = "event_data_too_large"
)

// Options bounds the bus. Zero fields take the package defaults.
type Options struct {
	// MaxPendingPerSession bounds undelivered events per session. Over the
	// bound the oldest are dropped and counted.
	MaxPendingPerSession int
	// MaxSummaryBytes bounds the one-line summary a model reads.
	MaxSummaryBytes int
	// MaxDataBytes bounds the structured payload.
	MaxDataBytes int
	// SubscriberBuffer sizes each live subscriber's channel.
	SubscriberBuffer int
	// Metrics records bounded event-drop telemetry. Nil disables observation.
	Metrics *metrics.Metrics
}

func (o Options) withDefaults() Options {
	if o.MaxPendingPerSession <= 0 {
		o.MaxPendingPerSession = defaultMaxPendingPerSession
	}

	if o.MaxSummaryBytes <= 0 {
		o.MaxSummaryBytes = defaultMaxSummaryBytes
	}

	if o.MaxDataBytes <= 0 {
		o.MaxDataBytes = defaultMaxDataBytes
	}

	if o.SubscriberBuffer <= 0 {
		o.SubscriberBuffer = defaultSubscriberBuffer
	}

	return o
}

// Bus is the one path from "something happened" to "the agent knows". It is
// session-scoped and bounded: publishing never blocks the publisher, because a
// finishing process must not stall on a session nobody is draining.
type Bus struct {
	options Options
	metrics *metrics.Metrics

	// scopelessContext carries the logging scope for Prepare and Publish,
	// which take no context. Their callers hold none to pass, and the bus
	// still has to log what it drops.
	scopelessContext context.Context //nolint:containedctx // See above.

	mutex       sync.Mutex
	pending     map[uuid.UUID]*queue
	subscribers map[uuid.UUID]map[uint64]chan Notice
	nextID      uint64
}

type queue struct {
	notices []Notice
	dropped int
}

// NewBus builds a bus with bounded per-session queues.
func NewBus(options Options) *Bus {
	return &Bus{
		options:          options.withDefaults(),
		metrics:          options.Metrics,
		scopelessContext: context.Background(),
		pending:          map[uuid.UUID]*queue{},
		subscribers:      map[uuid.UUID]map[uint64]chan Notice{},
	}
}

// EventLimits reports the size bounds this bus applies.
func (b *Bus) EventLimits() Limits {
	return Limits{
		MaxSummaryBytes: b.options.MaxSummaryBytes,
		MaxDataBytes:    b.options.MaxDataBytes,
	}
}

// Publish records one event for a session and hands it to any live
// subscribers. It validates the type but does not apply the reserved-prefix
// rule, because Peen's own producers publish reserved types through here. The
// HTTP boundary applies ValidateExternalType before calling this.
//
// A full queue drops its oldest event and counts the drop. A subscriber that is
// not reading is skipped rather than waited on.
func (b *Bus) Publish(notice Notice) (Notice, error) {
	return b.publish(b.scopelessContext, notice)
}

func (b *Bus) publish(ctx context.Context, notice Notice) (Notice, error) {
	notice, err := b.PrepareContext(ctx, notice)
	if err != nil {
		return Notice{}, err
	}

	b.mutex.Lock()
	defer b.mutex.Unlock()

	b.enqueue(notice)
	b.fanOut(notice)

	return notice, nil
}

// PublishContext lets a Bus satisfy Publisher. A Bus has no durable state, so
// callers that need replay use Runtime's publisher instead.
func (b *Bus) PublishContext(
	ctx context.Context,
	notice Notice,
) (Notice, error) {
	return b.publish(ctx, notice)
}

// Prepare validates and normalizes a notice without making it visible. Callers
// that need persistence-before-publication use it before their durable write.
// Its log lines carry no request scope; use PrepareContext when a context is
// at hand.
func (b *Bus) Prepare(notice Notice) (Notice, error) {
	return b.PrepareContext(b.scopelessContext, notice)
}

// PrepareContext is Prepare with a context, so the warning for content the bus
// has to cut carries the caller's request scope.
func (b *Bus) PrepareContext(
	ctx context.Context,
	notice Notice,
) (Notice, error) {
	if notice.SessionID == uuid.Nil {
		return Notice{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"event session is required",
		)
	}

	if err := ValidateType(notice.Type); err != nil {
		return Notice{}, err
	}

	delivery, err := ValidateDelivery(notice.Delivery)
	if err != nil {
		return Notice{}, err
	}

	notice.Delivery = delivery
	b.applyDefaults(ctx, &notice)

	if err := ValidateData(notice.Data); err != nil {
		return Notice{}, err
	}

	return notice, nil
}

// applyDefaults fills identity and time, then enforces the size bounds as a
// safety net. Outside callers and hooks are rejected before they get here, so
// what reaches the cut is Peen's own output (job and agent events).
func (b *Bus) applyDefaults(ctx context.Context, notice *Notice) {
	if notice.ID == uuid.Nil {
		notice.ID = uuid.New()
	}

	if notice.CreatedAt.IsZero() {
		notice.CreatedAt = time.Now().UTC()
	}

	notice.Summary = truncateToRuneBoundary(
		notice.Summary,
		b.options.MaxSummaryBytes,
	)

	if len(notice.Data) <= b.options.MaxDataBytes {
		return
	}

	ctxscope.GetLogger(ctx).Warn(
		"event data dropped",
		"reason", reasonEventDataTooLarge,
		"event_type", notice.Type,
		"session_id", notice.SessionID.String(),
		"size", len(notice.Data),
		"limit", b.options.MaxDataBytes,
	)

	notice.Data = nil
}

func (b *Bus) enqueue(notice Notice) {
	session, ok := b.pending[notice.SessionID]
	if !ok {
		session = &queue{}
		b.pending[notice.SessionID] = session
	}

	session.notices = append(session.notices, notice)

	for len(session.notices) > b.options.MaxPendingPerSession {
		session.notices = session.notices[1:]
		session.dropped++

		b.metrics.EventDropped("pending")
	}
}

// fanOut delivers to live subscribers without blocking. A subscriber whose
// buffer is full misses this event rather than stalling the publisher; the
// durable queue is what guarantees delivery.
func (b *Bus) fanOut(notice Notice) {
	for _, channel := range b.subscribers[notice.SessionID] {
		select {
		case channel <- notice:
		default:
			b.metrics.EventDropped("subscriber")
		}
	}
}

// Drain removes and returns everything pending for one session, plus how many
// were dropped since the previous drain. This is what a turn calls at a tool
// boundary to build one coalesced message.
func (b *Bus) Drain(sessionID uuid.UUID) Batch {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	session, ok := b.pending[sessionID]
	if !ok || len(session.notices) == 0 {
		return Batch{}
	}

	batch := Batch{Notices: session.notices, Dropped: session.dropped}

	delete(b.pending, sessionID)

	return batch
}

// Peek returns what is waiting for one session WITHOUT consuming it, so an
// operator reading the queue over HTTP does not steal events the agent has not
// been told about yet. Only Drain consumes.
func (b *Bus) Peek(sessionID uuid.UUID) Batch {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	session, ok := b.pending[sessionID]
	if !ok || len(session.notices) == 0 {
		return Batch{}
	}

	notices := make([]Notice, len(session.notices))
	copy(notices, session.notices)

	return Batch{Notices: notices, Dropped: session.dropped}
}

// Pending reports how many events are waiting for one session without
// consuming them.
func (b *Bus) Pending(sessionID uuid.UUID) int {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	session, ok := b.pending[sessionID]
	if !ok {
		return 0
	}

	return len(session.notices)
}

// Subscribe returns a channel of live events for one session and the function
// that stops the subscription. The returned function is safe to call more than
// once and closes the channel exactly once.
func (b *Bus) Subscribe(sessionID uuid.UUID) (<-chan Notice, func()) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	channel := make(chan Notice, b.options.SubscriberBuffer)

	if _, ok := b.subscribers[sessionID]; !ok {
		b.subscribers[sessionID] = map[uint64]chan Notice{}
	}

	b.nextID++
	id := b.nextID
	b.subscribers[sessionID][id] = channel

	stop := sync.OnceFunc(func() {
		b.unsubscribe(sessionID, id)
	})

	return channel, stop
}

func (b *Bus) unsubscribe(sessionID uuid.UUID, id uint64) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	session, ok := b.subscribers[sessionID]
	if !ok {
		return
	}

	channel, ok := session[id]
	if !ok {
		return
	}

	close(channel)
	delete(session, id)

	if len(session) == 0 {
		delete(b.subscribers, sessionID)
	}
}
