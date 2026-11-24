package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/session"
)

type turnDeadline struct {
	mutex         sync.Mutex
	timer         *time.Timer
	cancel        context.CancelFunc
	stopLifecycle func() bool
	fired         atomic.Bool
}

func (r *Runtime) withTurnDeadline(input TurnRequest) TurnRequest {
	if !input.Deadline.IsZero() {
		return input
	}

	timeout := r.turnTimeout
	if input.TimeoutSeconds != 0 {
		timeout = time.Duration(input.TimeoutSeconds) * time.Second
	}

	if timeout > 0 {
		input.Deadline = r.now().UTC().Add(timeout)
	}

	return input
}

func (p *preparedTurn) deadlineExpired() bool {
	return p.deadline != nil && p.deadline.fired.Load()
}

func (p *preparedTurn) stopDeadline() {
	p.deadline.stop()
}

func (d *turnDeadline) stop() {
	if d == nil {
		return
	}

	d.mutex.Lock()
	defer d.mutex.Unlock()

	d.timer.Stop()

	if d.stopLifecycle != nil {
		d.stopLifecycle()
	}

	d.cancel()
}

func (r *Runtime) armTurnDeadline(ctx context.Context, p *preparedTurn) {
	if p.input.Deadline.IsZero() || p.deadline != nil {
		return
	}

	lifetime := ctx
	ctx, cancel := context.WithCancel(ctx)
	state := &turnDeadline{cancel: cancel}
	p.deadline = state
	lease := p.lease

	state.mutex.Lock()
	state.timer = time.AfterFunc(time.Until(p.input.Deadline), func() {
		r.expireTurn(ctx, lease, state)
	})
	state.stopLifecycle = context.AfterFunc(lifetime, state.stop)
	state.mutex.Unlock()
}

func (r *Runtime) expireTurn(
	ctx context.Context,
	lease session.Lease,
	deadline *turnDeadline,
) {
	defer func() {
		if recover() != nil {
			ctxscope.GetLogger(ctx).Error("turn deadline callback panicked")
		}
	}()

	ticket := r.admissions.Reserve(lease.SessionID)
	defer ticket.Release()

	if err := ticket.Wait(ctx); err != nil {
		return
	}

	r.userMessageQueuesMutex.Lock()
	queue := r.userMessageQueues[lease.SessionID]
	isCurrent := queue != nil && queue.turn.lease.TurnID == lease.TurnID
	r.userMessageQueuesMutex.Unlock()

	if !isCurrent || ctx.Err() != nil {
		return
	}

	deadline.fired.Store(true)

	cleanup := context.WithoutCancel(ctx)
	_, cancelErr := r.CancelSession(cleanup, lease.SessionID)
	_, endErr := r.store.EndRunningTurn(cleanup, lease.SessionID,
		lease.TurnID, session.TimeoutTurnEnd())

	_, pauseErr := r.PauseSessionWorkflows(cleanup)
	if err := errors.Join(cancelErr, endErr, pauseErr); err != nil {
		ctxscope.GetLogger(cleanup).Error("turn deadline cancellation failed",
			"err", err)
	}
}
