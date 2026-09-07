package agent

import (
	"context"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const (
	wakeWindow           = time.Hour
	wakeMessageSeparator = "\n\n"

	reasonNoHandler      = "no_handler"
	reasonSessionBusy    = "session_busy"
	reasonWakeLimit      = "wake_rate_limit"
	reasonQueuedDelivery = "queued_delivery"
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

	published, err := r.eventBus.Publish(notice)
	if err != nil {
		return events.Notice{}, ctxerrors.Wrap(err, "publish session event")
	}

	r.considerWake(ctx, published)

	return published, nil
}

// considerWake decides whether this event starts a turn. It never fails the
// publish: a wake that cannot happen leaves the event queued.
func (r *Runtime) considerWake(ctx context.Context, notice events.Notice) {
	logger := ctxscope.GetLogger(ctx)

	if r.store.IsActive(notice.SessionID) {
		logger.Debug(
			"event wake degraded to queued delivery",
			"reason", reasonSessionBusy,
			"event_type", notice.Type,
		)

		return
	}

	workspace, handler, found, err := r.wakeHandler(ctx, notice)
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

	r.startWakeTurn(ctx, notice, workspace, handler)
}

// wakeHandler resolves the session's workspace and the handler declared for
// this event type. A session with no messages yet has no workspace to resolve
// against, so it cannot be woken.
func (r *Runtime) wakeHandler(
	ctx context.Context,
	notice events.Notice,
) (string, harness.EventHandler, bool, error) {
	workspace, err := r.lastWorkspace(ctx, notice.SessionID)
	if err != nil {
		return "", harness.EventHandler{}, false, err
	}

	if workspace == "" {
		return "", harness.EventHandler{}, false, nil
	}

	snapshot, err := r.resolver.Resolve(workspace)
	if err != nil {
		return "", harness.EventHandler{}, false, ctxerrors.Wrap(
			err,
			"resolve harness for event wake",
		)
	}

	handler, err := snapshot.EventHandler(notice.Type)
	if err != nil {
		// An unhandled type is the normal case, not a failure. The event
		// stays queued and is delivered at the next turn instead.
		return "", harness.EventHandler{}, false, nil //nolint:nilerr // Above.
	}

	return workspace, handler, true, nil
}

func (r *Runtime) lastWorkspace(
	ctx context.Context,
	sessionID uuid.UUID,
) (string, error) {
	page, err := r.store.ListMessages(
		ctx,
		sessionID,
		session.ListMessagesOptions{
			Order: session.PageOrderDescending,
			Limit: 1,
		},
	)
	if err != nil {
		return "", ctxerrors.Wrap(err, "read session workspace")
	}

	for _, message := range page.Items {
		if message.Workspace != "" {
			return message.Workspace, nil
		}
	}

	return r.defaultWorkspace, nil
}

// startWakeTurn runs the handler's instruction as a turn of its own. The
// context is detached because the caller that published the event, an HTTP
// request that is about to return 202, must not cancel the work it started.
func (r *Runtime) startWakeTurn(
	ctx context.Context,
	notice events.Notice,
	workspace string,
	handler harness.EventHandler,
) {
	sessionID := notice.SessionID
	detached := context.WithoutCancel(ctx)

	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				ctxscope.GetLogger(detached).Error(
					"event wake turn panicked",
					"event_type", notice.Type,
					"panic", recovered,
					"stack", string(debug.Stack()),
				)
			}
		}()

		ctxscope.GetLogger(detached).Info(
			"event wake turn started",
			"event_type", notice.Type,
			"session_id", sessionID.String(),
		)

		if _, err := r.Run(detached, TurnRequest{
			SessionID: &sessionID,
			Message:   wakeMessage(handler),
			Workspace: workspace,
			Origin: &TurnOrigin{
				EventID:   notice.ID,
				EventType: notice.Type,
			},
		}); err != nil {
			ctxscope.GetLogger(detached).Error(
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

// wakeMessage is the task the woken turn runs. The event itself is not pasted
// here: it is already queued and the turn delivers it as quoted data before its
// first model call, which keeps event text out of the instruction.
func wakeMessage(handler harness.EventHandler) string {
	instruction := strings.TrimSpace(handler.Instructions)
	if handler.Agent == "" {
		return instruction
	}

	return instruction + wakeMessageSeparator +
		"Handle this as the " + handler.Agent + " agent would."
}
