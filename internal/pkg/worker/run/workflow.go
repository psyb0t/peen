package run

import (
	"context"
	"errors"
	"sync"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/psyb0t/peen/internal/pkg/worker/protocol"
)

type workflowWorkerLifecycle interface {
	Start(context.Context) error
	WaitForCompletion() error
}

func startSessionWorkflows(
	ctx context.Context,
	runtime *agent.Runtime,
	connection *protocol.Connection,
	document worker.LaunchDocument,
) (func() error, error) {
	lifetime, cancel := context.WithCancel(ctx)
	storage := connection.Workflows()

	native, err := agent.NewWorkflowWorker(lifetime, runtime,
		storage, document.GenerationID, document.SessionID, nil)
	if err != nil {
		cancel()

		return nil, ctxerrors.Wrap(err, "assemble session workflows")
	}

	stop, err := startNativeWorkflow(lifetime, native)
	if err != nil {
		cancel()

		return nil, ctxerrors.Wrap(err, "start assembled workflow tasks")
	}

	return func() error {
		cancel()
		releaseWorkflowClaims(ctx, storage)

		return stop()
	}, nil
}

// releaseWorkflowClaims fails harmlessly when the connection is already
// gone, because the controller ends a closed connection's claims itself.
func releaseWorkflowClaims(
	ctx context.Context,
	storage *protocol.WorkflowBackend,
) {
	err := storage.ReleaseClaims(context.WithoutCancel(ctx))
	if err != nil {
		ctxscope.GetLogger(ctx).Debug("workflow claims not released",
			"err", err, "reason", "worker_stopping")
	}
}

func startNativeWorkflow(
	ctx context.Context,
	worker workflowWorkerLifecycle,
) (func() error, error) {
	lifetime, cancel := context.WithCancel(ctx)
	if err := worker.Start(lifetime); err != nil {
		cancel()

		return nil, errors.Join(
			ctxerrors.Wrap(err, "start native workflow tasks"),
			drainNativeWorkflow(worker))
	}

	return sync.OnceValue(func() error {
		cancel()

		return drainNativeWorkflow(worker)
	}), nil
}

func drainNativeWorkflow(worker workflowWorkerLifecycle) error {
	if err := worker.WaitForCompletion(); err != nil {
		return ctxerrors.Wrap(err, "drain native workflow tasks")
	}

	return nil
}

func (h *commandHandler) attachWorkflows(
	ctx context.Context,
	runtime *agent.Runtime,
	connection *protocol.Connection,
	document worker.LaunchDocument,
) error {
	stop, err := startSessionWorkflows(ctx, runtime, connection, document)
	if err != nil {
		return ctxerrors.Wrap(err, "start session workflow worker")
	}

	h.stopWorkflows = stop
	h.attach(runtime, connection.Store())

	return nil
}

func (h *commandHandler) serveSession(
	ctx context.Context,
	stop context.CancelFunc,
	served <-chan error,
	workspace string,
) error {
	ctxscope.GetLogger(ctx).Info("session worker ready", "workspace", workspace)
	serveErr := serve(ctx, stop, h, served)

	return errors.Join(serveErr, h.stopNativeWorkflows())
}

func (h *commandHandler) stopNativeWorkflows() error {
	if h.stopWorkflows == nil {
		return nil
	}

	return h.stopWorkflows()
}

func (h *commandHandler) cancelTurn(ctx context.Context) (any, error) {
	sessionID := h.runtime.SessionID()
	turnCancelled := h.store.RequestLocalCancellation(sessionID)
	result, cancelErr := h.runtime.CancelSession(ctx, sessionID)
	agentsCancelled := result != nil && result.CancelRequested

	workflowsPaused, err := h.runtime.PauseSessionWorkflows(ctx)
	if err != nil || cancelErr != nil {
		return nil, ctxerrors.Wrap(errors.Join(err, cancelErr),
			"pause worker session workflows")
	}

	return protocol.CancelResult{
		CancelRequested: turnCancelled || agentsCancelled || workflowsPaused,
	}, nil
}
