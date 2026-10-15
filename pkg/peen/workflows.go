package peen

import (
	"context"
	"errors"
	"sync"

	workflowworker "github.com/cschleiden/go-workflows/worker"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
	"github.com/psyb0t/peen/internal/pkg/session"
)

type embeddedWorkflows struct {
	mutex     sync.Mutex
	owner     *Runtime
	assembled *agent.Assembled
	sessions  map[uuid.UUID]*embeddedWorkflow
}

type embeddedWorkflow struct {
	runtime *agent.Runtime
	cancel  context.CancelFunc
	done    chan struct{}
	err     error
}

func newEmbeddedRuntime(
	assembled *agent.Assembled,
	configuration *embeddingConfiguration,
	options Options,
	hub *eventHub,
) (*Runtime, error) {
	runtime := &Runtime{
		internal:       assembled.Runtime,
		closeGate:      make(chan struct{}, 1),
		handle:         assembled.Handle,
		store:          assembled.Store,
		options:        options,
		temporaryState: configuration.temporaryState,
		eventHub:       hub,
		telemetry:      configuration.telemetry,
	}

	runtime.workflows = newEmbeddedWorkflows(runtime, assembled)
	if err := runtime.workflows.start(context.Background()); err != nil {
		return nil, errors.Join(
			ctxerrors.Wrap(err, "start embedded workflows"),
			runtime.abortStartup(),
		)
	}

	return runtime, nil
}

// No caller receives the runtime after startup fails, so cleanup cannot rely
// on a successful durable pause or a later Close retry.
func (r *Runtime) abortStartup() error {
	ctx := context.Background()
	result := cancelStartupOperations(ctx, r.internal)
	w := r.workflows
	w.mutex.Lock()

	entries := make([]*embeddedWorkflow, 0, len(w.sessions))
	for _, entry := range w.sessions {
		entry.cancel()
		result = errors.Join(result,
			cancelStartupOperations(ctx, entry.runtime))
		entries = append(entries, entry)
	}
	w.mutex.Unlock()

	for _, entry := range entries {
		<-entry.done
		result = errors.Join(result, entry.err,
			entry.runtime.Shutdown(ctx),
			entry.runtime.ShutdownAgentRuns(ctx),
			entry.runtime.ShutdownJobs(ctx))
	}

	result = errors.Join(result, r.internal.Shutdown(ctx),
		r.internal.ShutdownAgentRuns(ctx), r.internal.ShutdownJobs(ctx),
		r.handle.Close(), removeTemporaryState(r.temporaryState),
		r.telemetry.close())
	r.eventHub.close()
	r.closed.Store(true)

	return result
}

func cancelStartupOperations(
	ctx context.Context,
	runtime *agent.Runtime,
) error {
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	cancel()

	// Shutdown's cancelled-context path refuses admission and cancels calls.
	// The following shutdown with a live context waits for their final access.
	err := runtime.Shutdown(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}

	return ctxerrors.Wrap(err, "cancel failed startup operations")
}

func newEmbeddedWorkflows(owner *Runtime,
	assembled *agent.Assembled,
) *embeddedWorkflows {
	return &embeddedWorkflows{
		owner: owner, assembled: assembled,
		sessions: make(map[uuid.UUID]*embeddedWorkflow),
	}
}

func (w *embeddedWorkflows) start(ctx context.Context) error {
	var ids []uuid.UUID

	options := session.ListSessionsOptions{}
	for {
		page, err := w.owner.store.ListSessions(ctx, options)
		if err != nil {
			return ctxerrors.Wrap(err, "read embedded workflow sessions")
		}

		for _, item := range page.Items {
			ids = append(ids, item.ID)
		}

		if !page.HasMore {
			break
		}

		options.Offset += len(page.Items)
	}

	for _, id := range ids {
		if _, err := w.ensure(ctx, id); err != nil {
			return ctxerrors.Wrap(err, "start retained embedded session")
		}
	}

	return nil
}

func (w *embeddedWorkflows) ensure(ctx context.Context,
	id uuid.UUID,
) (*agent.Runtime, error) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if current := w.sessions[id]; current != nil {
		return current.runtime, nil
	}

	runtime := w.owner.internal
	if id != runtime.SessionID() {
		var err error

		runtime, err = w.assembled.NewSessionRuntime(ctx, id)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "create embedded session runtime")
		}
	}

	entry, err := w.startSession(ctx, runtime, id)
	if err != nil {
		cleanup := context.WithoutCancel(ctx)

		return nil, ctxerrors.Wrap(errors.Join(err,
			cancelStartupOperations(cleanup, runtime),
			runtime.Shutdown(cleanup),
			runtime.ShutdownAgentRuns(cleanup), runtime.ShutdownJobs(cleanup)),
			"start embedded session workflow")
	}

	w.sessions[id] = entry

	return runtime, nil
}

func (w *embeddedWorkflows) startSession(ctx context.Context,
	runtime *agent.Runtime, id uuid.UUID,
) (*embeddedWorkflow, error) {
	ctx = runtimecontext.WithLogger(ctx, w.owner.telemetry.logger)
	generation := uuid.New()

	backend, err := w.owner.handle.Workflows.WorkerBackend(ctx, generation)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "borrow embedded workflow storage")
	}

	lifecycle, cancel := context.WithCancel(context.WithoutCancel(ctx))

	worker, err := agent.NewWorkflowWorker(lifecycle, runtime, backend,
		generation, id, nil)
	if err != nil {
		cancel()

		return nil, ctxerrors.Wrap(err, "assemble embedded workflow worker")
	}

	if err := worker.Start(lifecycle); err != nil {
		cancel()

		return nil, ctxerrors.Wrap(errors.Join(err, worker.WaitForCompletion()),
			"start embedded workflow worker")
	}

	entry := &embeddedWorkflow{
		runtime: runtime, cancel: cancel, done: make(chan struct{}),
	}
	go entry.wait(lifecycle, worker)

	ctxscope.GetLogger(ctx).Info("embedded workflow worker started",
		"session_id", id)

	return entry, nil
}

func (e *embeddedWorkflow) wait(ctx context.Context,
	worker *workflowworker.Worker,
) {
	defer func() {
		if recover() != nil {
			e.err = ctxerrors.Wrap(commerr.ErrInvalidState,
				"embedded workflow worker panicked")
			ctxscope.GetLogger(ctx).Error("embedded workflow worker aborted",
				"err", e.err)
		}

		close(e.done)
	}()

	e.err = worker.WaitForCompletion()
	if e.err != nil {
		ctxscope.GetLogger(ctx).Error("embedded workflow worker stopped",
			"err", e.err)
	}
}

func (s *Session) core() *agent.Runtime {
	w := s.runtime.workflows
	if w == nil {
		return s.runtime.internal
	}

	w.mutex.Lock()
	defer w.mutex.Unlock()

	if current := w.sessions[s.id]; current != nil {
		return current.runtime
	}

	return s.runtime.internal
}

func (r *Runtime) shutdown(ctx context.Context) error {
	err := r.internal.Shutdown(ctx)
	if r.workflows != nil {
		err = errors.Join(err, r.workflows.shutdown(ctx))
	}

	if err != nil {
		return ctxerrors.Wrap(err, "shut down embedded execution")
	}

	return nil
}

func (w *embeddedWorkflows) shutdown(ctx context.Context) error {
	w.mutex.Lock()

	entries := make([]*embeddedWorkflow, 0, len(w.sessions))
	for _, entry := range w.sessions {
		entries = append(entries, entry)
	}
	w.mutex.Unlock()

	var result error

	for _, entry := range entries {
		if err := entry.runtime.Shutdown(ctx); err != nil {
			result = errors.Join(result,
				ctxerrors.Wrap(err, "drain embedded session"))
		}

		entry.cancel()

		select {
		case <-entry.done:
			result = errors.Join(result, entry.err)
		case <-ctx.Done():
			result = errors.Join(result,
				ctxerrors.Wrap(ctx.Err(), "drain embedded worker"))
		}
	}

	return result
}
