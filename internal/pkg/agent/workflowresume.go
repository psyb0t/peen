package agent

import "github.com/psyb0t/go-workflows/workflow"

// workflowResumeGateKey carries one execution's resumeGate on its workflow
// context.
type workflowResumeGateKey struct{}

// resumeGate hands one resume signal to every step waiting on it.
//
// Parallel tool steps of one execution can all be paused at once, and a
// resume sends one signal. Without the gate the first step to receive it
// continues and the others wait for a signal that never comes. Workflow
// coroutines run one at a time, so the gate needs no lock.
type resumeGate struct {
	// epoch counts the resume signals received so far. A step reads it
	// before it rechecks its durable decision, so a signal that arrives
	// during the recheck is not missed.
	epoch   int
	waiters []workflow.Channel[struct{}]
}

//nolint:ireturn // workflow.Context is the library's context type.
func withResumeGate(ctx workflow.Context) workflow.Context {
	return workflow.WithValue(ctx, workflowResumeGateKey{}, &resumeGate{})
}

// workflowResumeGate returns the execution's gate, or a gate of its own for
// a context that has none, which waits for a signal as a lone step would.
func workflowResumeGate(ctx workflow.Context) *resumeGate {
	gate, ok := ctx.Value(workflowResumeGateKey{}).(*resumeGate)
	if !ok {
		return &resumeGate{}
	}

	return gate
}

// wait returns once a resume signal arrives after seen, the epoch the caller
// read before its last recheck. It returns false when the signal channel is
// closed because the workflow was cancelled.
func (g *resumeGate) wait(ctx workflow.Context, seen int) bool {
	if g.epoch > seen {
		return true
	}

	woken := workflow.NewBufferedChannel[struct{}](1)
	g.waiters = append(g.waiters, woken)

	received := true

	workflow.Select(ctx,
		workflow.Receive(
			workflow.NewSignalChannel[struct{}](ctx, workflowResumeSignal),
			func(_ workflow.Context, _ struct{}, ok bool) {
				received = ok
				if ok {
					g.broadcast()
				}
			},
		),
		workflow.Receive(
			woken,
			func(workflow.Context, struct{}, bool) {},
		),
	)

	return received
}

// broadcast records one received signal and wakes every waiting step.
func (g *resumeGate) broadcast() {
	g.epoch++

	for _, waiter := range g.waiters {
		waiter.SendNonblocking(struct{}{})
	}

	g.waiters = nil
}
