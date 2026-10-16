package control

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// feedPumpPageLimit bounds one ListFeed read inside the pump loop.
const feedPumpPageLimit = session.MaximumFeedLimit

// FeedSink delivers one session's feed rows to live clients, in sequence
// order. The pump calls it once per page it reads, never out of order and
// never with a row skipped.
type FeedSink func(
	ctx context.Context,
	sessionID uuid.UUID,
	items []session.FeedItem,
)

// feedStore is the store surface the pump reads through. It is an interface
// so the pump depends on reading a feed page rather than on *session.Store.
type feedStore interface {
	ListFeed(
		ctx context.Context,
		sessionID uuid.UUID,
		cursor int64,
		limit int,
	) (*session.FeedPage, error)
}

// FeedPump carries every committed feed row from the durable store to the
// live feed, in sequence order, and reports when a session's worker admits a
// message.
//
// It exists because the store signals that a session's feed gained rows, but
// it must not run the delivery read on the writer's own goroutine: a page
// read and a live broadcast both cost more than a SQLite write should wait
// for. One pump goroutine per session, single-flight, carries that work
// instead. control-core builds the pump over its store and service context,
// then control-api registers the sink once the WebSocket hub exists, the
// same handoff order the previous event relay used.
//
// Reading and admission release do not depend on a sink. Before control-api
// registers one, the pump still reads every signalled page and releases the
// admissions it finds; it only has no live clients to hand the rows to.
type FeedPump struct {
	//nolint:containedctx // Bounds every pump goroutine's lifetime.
	ctx context.Context

	store feedStore

	sinkMutex sync.RWMutex
	sink      FeedSink

	stateMutex sync.Mutex
	sessions   map[uuid.UUID]*feedPumpSession

	admissionMutex   sync.Mutex
	admissionWatches map[admissionKey]*admissionWatch
}

// feedPumpSession is one session's delivery cursor and single-flight state.
type feedPumpSession struct {
	lastPublished int64
	initialized   bool
	running       bool
	dirty         bool
}

// admissionKey names one message: the session it was sent to and its request.
type admissionKey struct {
	sessionID uuid.UUID
	requestID uuid.UUID
}

// admissionWatch is one caller waiting to hear a message was admitted. It is a
// pointer so a stop can tell its own watch from a later one under the same key.
type admissionWatch struct {
	admitted func()
}

// NewFeedPump builds a pump bound to ctx. Every pump goroutine stops when ctx
// ends, so callers pass their service context, never context.Background.
func NewFeedPump(ctx context.Context, store feedStore) *FeedPump {
	return &FeedPump{
		ctx:              ctx,
		store:            store,
		sessions:         map[uuid.UUID]*feedPumpSession{},
		admissionWatches: map[admissionKey]*admissionWatch{},
	}
}

// SetSink registers the live delivery path. Registering twice replaces it,
// which is what a restarted API service needs.
func (p *FeedPump) SetSink(sink FeedSink) {
	p.sinkMutex.Lock()
	defer p.sinkMutex.Unlock()

	p.sink = sink
}

func (p *FeedPump) getSink() FeedSink {
	p.sinkMutex.RLock()
	defer p.sinkMutex.RUnlock()

	return p.sink
}

// WatchAdmission calls admitted once the session's worker admits the message
// sent under requestID.
//
// A worker records userMessage.created for a message exactly when it admits
// it, whether the message starts a turn or joins the running turn's queue, and
// the record passes through this pump's delivery loop. admitted runs at most
// once, on the pump's session goroutine, so it must not block. The returned
// stop ends the watch and is safe to call after admitted has run.
func (p *FeedPump) WatchAdmission(
	sessionID uuid.UUID,
	requestID uuid.UUID,
	admitted func(),
) func() {
	key := admissionKey{sessionID: sessionID, requestID: requestID}
	watch := &admissionWatch{admitted: admitted}

	p.admissionMutex.Lock()
	p.admissionWatches[key] = watch
	p.admissionMutex.Unlock()

	return func() {
		p.admissionMutex.Lock()
		defer p.admissionMutex.Unlock()

		if p.admissionWatches[key] == watch {
			delete(p.admissionWatches, key)
		}
	}
}

// Signal notifies the pump that sessionID's feed gained rows starting at
// first. It is the function session.Store.SetFeedSignal calls, once per
// commit, strictly after the writing transaction returned nil.
//
// The first signal a session receives sets its cursor to first-1, so a
// controller that starts with history already stored reads only new rows.
// Admission release must never wait on a client being able to watch, so
// Signal reads whether or not a sink is registered.
func (p *FeedPump) Signal(sessionID uuid.UUID, first int64) {
	p.stateMutex.Lock()

	state, exists := p.sessions[sessionID]
	if !exists {
		state = &feedPumpSession{}
		p.sessions[sessionID] = state
	}

	if !state.initialized {
		state.initialized = true
		state.lastPublished = first - 1
	}

	if state.running {
		state.dirty = true
		p.stateMutex.Unlock()

		return
	}

	state.running = true
	p.stateMutex.Unlock()

	go p.run(sessionID, state)
}

// run drains sessionID's feed from state.lastPublished until the sink has
// seen everything committed, then exits. Exactly one of these runs per
// session at a time: Signal starts it once and marks later signals dirty
// instead of starting a second one.
func (p *FeedPump) run(sessionID uuid.UUID, state *feedPumpSession) {
	ctx := ctxscope.Set(p.ctx, ctxscope.Attr("session_id", sessionID.String()))

	defer func() {
		if recovered := recover(); recovered != nil {
			ctxscope.GetLogger(ctx).Error(
				"feed pump goroutine panicked", "panic", recovered,
			)
			p.markStopped(state)
		}
	}()

	for {
		if p.ctx.Err() != nil {
			p.markStopped(state)

			return
		}

		if !p.runOnce(ctx, sessionID, state) {
			return
		}
	}
}

// runOnce reads and delivers one feed page for sessionID. It reports whether
// run should keep looping: false means it stopped, either on a read failure
// or because nothing is left to read and no dirty re-signal arrived.
func (p *FeedPump) runOnce(
	ctx context.Context,
	sessionID uuid.UUID,
	state *feedPumpSession,
) bool {
	p.stateMutex.Lock()
	cursor := state.lastPublished
	p.stateMutex.Unlock()

	page, err := p.store.ListFeed(ctx, sessionID, cursor, feedPumpPageLimit)
	if err != nil {
		p.logReadFailure(ctx, err, cursor)
		p.markStopped(state)

		return false
	}

	p.deliver(ctx, sessionID, page.Items)

	p.stateMutex.Lock()
	defer p.stateMutex.Unlock()

	state.lastPublished = page.NextCursor

	if page.HasMore {
		return true
	}

	if state.dirty {
		state.dirty = false

		return true
	}

	state.running = false

	return false
}

// logReadFailure reports a failed feed read. A context cancelled mid-read is
// the pump stopping with the service, not a failure the next signal should be
// blamed for, so that case logs at Debug rather than Error.
func (p *FeedPump) logReadFailure(
	ctx context.Context,
	err error,
	cursor int64,
) {
	wrapped := ctxerrors.Wrap(err, "read session feed page")
	logger := ctxscope.GetLogger(ctx)

	if p.ctx.Err() != nil {
		logger.Debug("feed pump read stopped by context cancellation",
			"err", wrapped, "cursor", cursor)

		return
	}

	logger.Error("feed pump read failed", "err", wrapped, "cursor", cursor)
}

// markStopped records that no goroutine is running for state's session, so
// the next signal starts a fresh one instead of assuming one is still live.
func (p *FeedPump) markStopped(state *feedPumpSession) {
	p.stateMutex.Lock()
	state.running = false
	p.stateMutex.Unlock()
}

// deliver releases every admission watch a page's items satisfy, then hands
// the page to the sink in order when one is registered.
func (p *FeedPump) deliver(
	ctx context.Context,
	sessionID uuid.UUID,
	items []session.FeedItem,
) {
	if len(items) == 0 {
		return
	}

	p.reportAdmissions(sessionID, items)

	sink := p.getSink()
	if sink == nil {
		return
	}

	sink(ctx, sessionID, items)
}

func (p *FeedPump) reportAdmissions(
	sessionID uuid.UUID,
	items []session.FeedItem,
) {
	for _, item := range items {
		if item.Kind != session.FeedKindEvent ||
			item.EventType != agent.EventTypeUserMessageCreated {
			continue
		}

		key := admissionKey{sessionID: sessionID, requestID: item.RequestID}

		p.admissionMutex.Lock()
		watch, found := p.admissionWatches[key]
		delete(p.admissionWatches, key)
		p.admissionMutex.Unlock()

		if found {
			watch.admitted()
		}
	}
}
