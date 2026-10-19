package agent

import (
	"context"
	"runtime/debug"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// wireIdleWakeRecheck installs the runtime's idle re-check on a store that
// offers one, mirroring how SetFeedSignal is wired. A store under test that
// does not implement IdleNotifier simply never re-checks, which is correct
// for a store nobody reads a wake decision against.
func (r *Runtime) wireIdleWakeRecheck() {
	notifier, ok := r.store.(session.IdleNotifier)
	if !ok {
		return
	}

	notifier.SetIdleCallback(r.handleSessionIdle)
}

// handleSessionIdle re-checks a session's still-pending notices once its turn
// ends in state, outside the store's own locks.
//
// A notice published while that turn was finishing degrades to a queued
// delivery because considerWake saw the session as busy at that moment, and
// nothing else revisits it once the turn is gone: the turn that published it
// has no tool boundary left to deliver it at, and it ends without consuming
// it. Only a turn that ended completed or failed on its own re-checks. A turn
// Stop cancelled, or one a crash recovery paused or interrupted, must not be
// followed by a fresh wake turn for events that landed during that stop, so
// those terminal states are left alone here.
func (r *Runtime) handleSessionIdle(
	sessionID uuid.UUID,
	state models.TurnState,
) {
	if state != models.TurnStateCompleted && state != models.TurnStateFailed {
		return
	}

	// A wake turn of this session is still between its decision and its
	// return, possibly the very turn that just ended. Its release runs this
	// re-check, so a second wake decision cannot race it here.
	if r.wakes.deferRecheck(sessionID) {
		return
	}

	// There is no caller context to inherit: this fires from inside the
	// store's own bookkeeping, strictly after the turn that triggered it has
	// already fully ended. context.Background() is the correct root for a
	// new, independent background operation, the same reasoning
	// startWakeTurn uses to detach from the request that published an event.
	detached, finish, err := r.BeginOperation(context.Background())
	if err != nil {
		return
	}

	// The pending read runs here, before the finished turn returns to its
	// caller, so a session with nothing pending starts no background work.
	// That is the common case, and it leaves no goroutine reading runtime
	// state after the turn is over.
	pending, err := r.pendingSessionNotices(detached, sessionID)
	if err != nil {
		runtimecontext.Logger(detached).Warn(
			"idle wake recheck could not read pending notices",
			"session_id", sessionID.String(),
			"err", err,
		)
		finish()

		return
	}

	if len(pending) == 0 {
		finish()

		return
	}

	go func() {
		defer finish()
		defer func() {
			if recovered := recover(); recovered != nil {
				runtimecontext.Logger(detached).Error(
					"idle wake recheck panicked",
					"session_id", sessionID.String(),
					"panic", recovered,
					"stack", string(debug.Stack()),
				)
			}
		}()

		r.wakeFirstPending(detached, sessionID, pending)
	}()
}

// recheckIdleWake looks at sessionID's pending notices, oldest first, and
// runs the normal wake decision for each until one starts a turn. The woken
// turn's own opening consumes every notice still pending, so finding one that
// starts a turn is enough: the rest ride along with it instead of each
// starting their own.
func (r *Runtime) recheckIdleWake(ctx context.Context, sessionID uuid.UUID) {
	logger := runtimecontext.Logger(ctx)

	pending, err := r.pendingSessionNotices(ctx, sessionID)
	if err != nil {
		logger.Warn(
			"idle wake recheck could not read pending notices",
			"session_id", sessionID.String(),
			"err", err,
		)

		return
	}

	if len(pending) == 0 {
		logger.Debug(
			"idle wake recheck found no pending notices",
			"session_id", sessionID.String(),
		)

		return
	}

	r.wakeFirstPending(ctx, sessionID, pending)
}

// wakeFirstPending runs the normal wake decision for each pending notice,
// oldest first, until one starts a turn.
func (r *Runtime) wakeFirstPending(
	ctx context.Context,
	sessionID uuid.UUID,
	pending []events.Notice,
) {
	logger := runtimecontext.Logger(ctx)

	for _, notice := range pending {
		if r.considerWake(ctx, notice) {
			return
		}
	}

	logger.Debug(
		"idle wake recheck left every pending notice queued",
		"session_id", sessionID.String(),
		"pending_count", len(pending),
	)
}

// pendingSessionNotices reads every notice this session still has pending, in
// durable sequence order, across as many pages as it takes.
func (r *Runtime) pendingSessionNotices(
	ctx context.Context,
	sessionID uuid.UUID,
) ([]events.Notice, error) {
	options := session.ListSessionNoticesOptions{
		State: models.NoticeStatePending,
	}

	var notices []events.Notice

	for {
		page, err := r.store.ListSessionNotices(ctx, sessionID, options)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "list pending session notices")
		}

		notices = append(
			notices,
			sessionNoticeBatch(page.Items).Notices...,
		)

		if !page.HasMore {
			return notices, nil
		}

		options.Offset += len(page.Items)
	}
}
