package agent

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
)

type runtimeLifecycle struct {
	mutex      sync.Mutex
	closing    bool
	operations map[uuid.UUID]context.CancelFunc
	drained    chan struct{}
}

// BeginOperation prevents admission after shutdown and tracks storage users.
// The caller must invoke finish after its last access to runtime resources.
func (r *Runtime) BeginOperation(
	ctx context.Context,
) (context.Context, func(), error) {
	ctx = runtimecontext.WithLogger(ctx, r.logger)
	lifecycle := &r.lifecycle
	lifecycle.mutex.Lock()
	defer lifecycle.mutex.Unlock()

	if lifecycle.closing {
		return nil, nil, ctxerrors.Wrap(commerr.ErrClosed, "runtime closed")
	}

	if lifecycle.operations == nil {
		lifecycle.operations = map[uuid.UUID]context.CancelFunc{}
	}

	id := uuid.New()
	operation, cancel := context.WithCancel(ctx)
	lifecycle.operations[id] = cancel

	var once sync.Once

	finish := func() {
		once.Do(func() {
			cancel()
			lifecycle.mutex.Lock()
			defer lifecycle.mutex.Unlock()

			delete(lifecycle.operations, id)

			if lifecycle.closing && len(lifecycle.operations) == 0 {
				close(lifecycle.drained)
			}
		})
	}

	return operation, finish, nil
}

func (r *Runtime) isClosing() bool {
	r.lifecycle.mutex.Lock()
	defer r.lifecycle.mutex.Unlock()

	return r.lifecycle.closing
}

// Shutdown refuses new work, drains admitted calls, then stops children and
// jobs. A deadline cancels admitted calls and leaves resources open for retry.
func (r *Runtime) Shutdown(ctx context.Context) error {
	ctx = runtimecontext.WithLogger(ctx, r.logger)
	lifecycle := &r.lifecycle
	lifecycle.mutex.Lock()
	if !lifecycle.closing {
		lifecycle.closing = true

		lifecycle.drained = make(chan struct{})
		if len(lifecycle.operations) == 0 {
			close(lifecycle.drained)
		}
	}

	drained := lifecycle.drained
	lifecycle.mutex.Unlock()

	if ctx.Err() != nil {
		r.cancelOperations()

		return ctxerrors.Wrap(ctx.Err(), "drain runtime operations")
	}

	select {
	case <-drained:
	case <-ctx.Done():
		r.cancelOperations()

		return ctxerrors.Wrap(ctx.Err(), "drain runtime operations")
	}

	if _, err := r.PauseSessionWorkflows(ctx); err != nil {
		return ctxerrors.Wrap(err, "pause closing runtime workflows")
	}

	if err := r.ShutdownAgentRuns(ctx); err != nil {
		return ctxerrors.Wrap(err, "stop runtime children")
	}

	if err := r.ShutdownJobs(ctx); err != nil {
		return ctxerrors.Wrap(err, "stop runtime jobs")
	}

	return nil
}

func (r *Runtime) cancelOperations() {
	r.lifecycle.mutex.Lock()
	defer r.lifecycle.mutex.Unlock()

	for _, cancel := range r.lifecycle.operations {
		cancel()
	}
}
