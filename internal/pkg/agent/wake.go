package agent

import (
	"context"
	"runtime/debug"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const (
	wakeWindow = time.Hour

	reasonNoHandler      = "no_handler"
	reasonSessionBusy    = "session_busy"
	reasonWakeLimit      = "wake_rate_limit"
	reasonQueuedDelivery = "queued_delivery"

	//nolint:lll // Instruction text is one line.
	agentWakeInstructions = "A child agent you launched reported back. Read its event in this turn and continue the work it belongs to."
)

// wakeLimiter bounds how often events may start turns for one session. A
// webhook pointed at a busy application can publish without end, and each
// started turn spends money, so the bound is on Peen's side rather than the
// caller's good behavior.
type wakeLimiter struct {
	limit int

	mutex sync.Mutex
	seen  map[uuid.UUID][]time.Time
}

type runtimeEventPublisher struct {
	runtime *Runtime
}

func (p runtimeEventPublisher) PublishContext(
	ctx context.Context,
	notice events.Notice,
) (events.Notice, error) {
	return p.runtime.PublishEvent(ctx, notice)
}

// durableEventPublisher makes every internal producer use the same
// persistence-before-live path as externally published notices.
//
//nolint:ireturn // The interface must be nil when no bus is configured.
func (r *Runtime) durableEventPublisher() events.Publisher {
	if r.eventBus == nil {
		return nil
	}

	return runtimeEventPublisher{runtime: r}
}

func newWakeLimiter(limit int) *wakeLimiter {
	return &wakeLimiter{limit: limit, seen: map[uuid.UUID][]time.Time{}}
}

// allow reports whether one more wake fits in the window and records it when
// it does.
func (w *wakeLimiter) allow(sessionID uuid.UUID, now time.Time) bool {
	if w.limit <= 0 {
		return true
	}

	w.mutex.Lock()
	defer w.mutex.Unlock()

	cutoff := now.Add(-wakeWindow)
	kept := make([]time.Time, 0, len(w.seen[sessionID]))

	for _, at := range w.seen[sessionID] {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}

	if len(kept) >= w.limit {
		w.seen[sessionID] = kept

		return false
	}

	w.seen[sessionID] = append(kept, now)

	return true
}

// PublishEvent records an event for one existing session. When the event asks
// to wake the session and a handler declares what to do with that type, it also
// starts a turn so the agent acts on it without anyone sending a message.
//
// Everything that prevents a wake degrades to a queued delivery rather than an
// error: a busy session, an unhandled type, or a session over its wake rate all
// leave the event waiting for the next turn.
func (r *Runtime) PublishEvent(
	ctx context.Context,
	notice events.Notice,
) (events.Notice, error) {
	if r.eventBus == nil {
		return events.Notice{}, ctxerrors.Wrap(
			ErrEventsUnavailable,
			"no event bus is configured",
		)
	}

	if _, err := r.store.Get(ctx, notice.SessionID); err != nil {
		return events.Notice{}, ctxerrors.Wrap(err, "resolve event session")
	}

	prepared, err := r.eventBus.Prepare(notice)
	if err != nil {
		return events.Notice{}, ctxerrors.Wrap(err, "prepare session event")
	}

	stored, err := r.store.CreateSessionNotice(
		ctx,
		prepared.SessionID,
		session.CreateSessionNoticeInput{
			ID:        prepared.ID,
			Type:      prepared.Type,
			Source:    prepared.Source,
			Summary:   prepared.Summary,
			DataJSON:  string(prepared.Data),
			Delivery:  models.NoticeDelivery(prepared.Delivery),
			CreatedAt: prepared.CreatedAt,
		},
	)
	if err != nil {
		return events.Notice{}, ctxerrors.Wrap(err, "persist session event")
	}

	published, err := r.eventBus.Publish(prepared)
	if err != nil {
		return events.Notice{}, ctxerrors.Wrap(err, "publish session event")
	}

	if published.ID != stored.ID {
		return events.Notice{}, ctxerrors.Wrap(
			commerr.ErrInvalidState,
			"published session event identity",
		)
	}

	r.considerWake(ctx, published)

	return published, nil
}

// considerWake decides whether this event starts a turn. It never fails the
// publish: a wake that cannot happen leaves the event queued.
func (r *Runtime) considerWake(ctx context.Context, notice events.Notice) {
	if r.isClosing() {
		return
	}

	logger := runtimecontext.Logger(ctx)

	if r.store.IsActive(notice.SessionID) {
		logger.Debug(
			"event wake degraded to queued delivery",
			"reason", reasonSessionBusy,
			"event_type", notice.Type,
		)

		return
	}

	handler, found, err := r.wakeHandler(ctx, notice)
	if err != nil {
		logger.Warn(
			"event wake handler lookup failed, event stays queued",
			"event_type", notice.Type,
			"err", err,
		)

		return
	}

	if !found {
		logger.Debug(
			"event wake degraded to queued delivery",
			"reason", reasonNoHandler,
			"event_type", notice.Type,
		)

		return
	}

	// The handler decides last. A type whose handler declares wake starts a
	// turn even when the producer said queue, which is the whole point of the
	// setting: the deployment owns what a type means, not whoever posted it.
	if !wakeRequested(notice, handler) {
		logger.Debug(
			"event stays queued",
			"reason", reasonQueuedDelivery,
			"event_type", notice.Type,
		)

		return
	}

	if !r.wakes.allow(notice.SessionID, time.Now()) {
		logger.Warn(
			"event wake degraded to queued delivery",
			"reason", reasonWakeLimit,
			"event_type", notice.Type,
		)

		return
	}

	r.startWakeTurn(ctx, notice, handler)
}

// wakeHandler resolves the handler for this event type from the workspace of
// the session the notice belongs to. It reads that session rather than the
// runtime's own, because a control surface serves many workspaces and each one
// declares its own handlers.
func (r *Runtime) wakeHandler(
	ctx context.Context,
	notice events.Notice,
) (harness.EventHandler, bool, error) {
	stored, err := r.store.Get(ctx, notice.SessionID)
	if err != nil {
		return harness.EventHandler{}, false, ctxerrors.Wrap(
			err,
			"load session for event wake",
		)
	}

	snapshot, err := r.resolver.Resolve(stored.Workspace)
	if err != nil {
		return harness.EventHandler{}, false, ctxerrors.Wrap(
			err,
			"resolve harness for event wake",
		)
	}

	handler, err := snapshot.EventHandler(notice.Type)
	if err == nil {
		return handler, true, nil
	}

	// A child agent reporting to the root has a handler without any file:
	// the root launched it to hear back. A workspace handler for the type
	// still wins, and its delivery can hold these events back.
	if notice.Source == agentEventSource {
		return harness.EventHandler{
			Type:         notice.Type,
			Instructions: agentWakeInstructions,
		}, true, nil
	}

	// An unhandled type is the normal case, not a failure. The event stays
	// queued and is delivered at the next turn instead.
	return harness.EventHandler{}, false, nil
}

// WakeRunner runs a turn a session event started. A controller sets one that
// sends the turn to the session's worker, so a woken turn runs under the
// session's agent space and streams to clients like any other turn.
type WakeRunner func(
	ctx context.Context,
	sessionID uuid.UUID,
	request MessageRequest,
) error

// SetWakeRunner routes woken turns through runner instead of this runtime.
// Without one, the runtime runs woken turns itself, which is right only when
// it is the process that runs the session's turns.
func (r *Runtime) SetWakeRunner(runner WakeRunner) {
	r.wakeRunnerMutex.Lock()
	defer r.wakeRunnerMutex.Unlock()

	r.wakeRunner = runner
}

func (r *Runtime) currentWakeRunner() WakeRunner {
	r.wakeRunnerMutex.RLock()
	defer r.wakeRunnerMutex.RUnlock()

	if r.wakeRunner != nil {
		return r.wakeRunner
	}

	return r.runWakeTurn
}

// runWakeTurn runs a woken turn in this runtime.
func (r *Runtime) runWakeTurn(
	ctx context.Context,
	_ uuid.UUID,
	request MessageRequest,
) error {
	if _, err := r.RunMessage(ctx, request, uuid.New(), nil); err != nil {
		return ctxerrors.Wrap(err, "run the woken turn")
	}

	return nil
}

// startWakeTurn runs the handler's instruction as a turn of its own. The
// context is detached because the caller that published the event, an HTTP
// request that is about to return 202, must not cancel the work it started.
func (r *Runtime) startWakeTurn(
	ctx context.Context,
	notice events.Notice,
	handler harness.EventHandler,
) {
	sessionID := notice.SessionID

	detached, finish, err := r.BeginOperation(context.WithoutCancel(ctx))
	if err != nil {
		return
	}

	runner := r.currentWakeRunner()

	go func() {
		defer finish()
		defer func() {
			if recovered := recover(); recovered != nil {
				runtimecontext.Logger(detached).Error(
					"event wake turn panicked",
					"event_type", notice.Type,
					"panic", recovered,
					"stack", string(debug.Stack()),
				)
			}
		}()

		runtimecontext.Logger(detached).Info(
			"event wake turn started",
			"event_type", notice.Type,
			"session_id", sessionID.String(),
		)

		message, err := wakeMessage(handler)
		if err != nil {
			runtimecontext.Logger(detached).Error(
				"event wake message could not be built",
				"event_type", notice.Type,
				"session_id", sessionID.String(),
				"err", err,
			)

			return
		}

		if err := runner(detached, sessionID, MessageRequest{
			SessionID: &sessionID,
			Message:   message,
			Origin: &TurnOrigin{
				EventID:   notice.ID,
				EventType: notice.Type,
			},
		}); err != nil {
			runtimecontext.Logger(detached).Error(
				"event wake turn failed",
				"event_type", notice.Type,
				"session_id", sessionID.String(),
				"err", err,
			)
		}
	}()
}

// wakeRequested reports whether this event should start a turn.
//
// A handler's declared delivery overrides the notice's, in either direction:
// it can promote a queued type to a wake, and it can hold a wake-marked event
// back to the next turn. An empty handler delivery leaves the notice's own
// mode in force.
func wakeRequested(
	notice events.Notice,
	handler harness.EventHandler,
) bool {
	switch handler.Delivery {
	case events.DeliveryWake:
		return true
	case events.DeliveryQueue:
		return false
	default:
		return notice.Delivery == events.DeliveryWake
	}
}

// wakeMessage is the task the woken turn runs, wrapped in a handler
// block. The event itself is not pasted here: it is already queued and the turn
// delivers it as quoted data before its first model call, which keeps event
// text out of the instruction.
func wakeMessage(handler harness.EventHandler) (string, error) {
	return renderEventHandler(handler)
}
