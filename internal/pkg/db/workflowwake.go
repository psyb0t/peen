package db

import (
	"context"
	"sync"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/go-workflows/backend"
	"github.com/psyb0t/go-workflows/backend/history"
	"github.com/psyb0t/go-workflows/core"
	"github.com/psyb0t/go-workflows/diag"
	"github.com/psyb0t/go-workflows/workflow"
)

// workflowWake tells waiting task claims that a write may have made a task
// ready. Without it a worker sleeps its whole polling interval after every
// empty claim, and each step of a turn pays that sleep several times.
type workflowWake struct {
	mutex   sync.Mutex
	pending chan struct{}
}

func newWorkflowWake() *workflowWake {
	return &workflowWake{pending: make(chan struct{})}
}

// signal returns the channel the next write closes. Take it before a claim,
// so a write that lands between the claim and the wait still wakes it.
func (w *workflowWake) signal() <-chan struct{} {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	return w.pending
}

func (w *workflowWake) notify() {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	close(w.pending)
	w.pending = make(chan struct{})
}

// wakingBackend wakes task claims after each write and lets an empty claim
// wait for one. The wait is bounded because some tasks become ready by time
// alone, such as a timer firing or an abandoned lease expiring.
type wakingBackend struct {
	diag.Backend

	wake *workflowWake
}

func (b *wakingBackend) CreateWorkflowInstance(
	ctx context.Context,
	instance *workflow.Instance,
	event *history.Event,
) error {
	defer b.wake.notify()

	err := b.Backend.CreateWorkflowInstance(ctx, instance, event)
	if err != nil {
		return ctxerrors.Wrap(err, "create workflow instance")
	}

	return nil
}

func (b *wakingBackend) CancelWorkflowInstance(
	ctx context.Context,
	instance *workflow.Instance,
	event *history.Event,
) error {
	defer b.wake.notify()

	err := b.Backend.CancelWorkflowInstance(ctx, instance, event)
	if err != nil {
		return ctxerrors.Wrap(err, "cancel workflow instance")
	}

	return nil
}

func (b *wakingBackend) SignalWorkflow(
	ctx context.Context,
	instanceID string,
	event *history.Event,
) error {
	defer b.wake.notify()

	err := b.Backend.SignalWorkflow(ctx, instanceID, event)
	if err != nil {
		return ctxerrors.Wrap(err, "signal workflow instance")
	}

	return nil
}

func (b *wakingBackend) CompleteWorkflowTask(
	ctx context.Context,
	task *backend.WorkflowTask,
	state core.WorkflowInstanceState,
	executed, activities, timers []*history.Event,
	targets []*history.WorkflowEvent,
) error {
	defer b.wake.notify()

	err := b.Backend.CompleteWorkflowTask(ctx, task, state,
		executed, activities, timers, targets)
	if err != nil {
		return ctxerrors.Wrap(err, "complete workflow task")
	}

	return nil
}

func (b *wakingBackend) CompleteActivityTask(
	ctx context.Context,
	task *backend.ActivityTask,
	result *history.Event,
) error {
	defer b.wake.notify()

	err := b.Backend.CompleteActivityTask(ctx, task, result)
	if err != nil {
		return ctxerrors.Wrap(err, "complete workflow activity")
	}

	return nil
}

func (b *wakingBackend) GetWorkflowTask(
	ctx context.Context,
	queues []workflow.Queue,
) (*backend.WorkflowTask, error) {
	return claimWhenReady(ctx, b.wake,
		func() (*backend.WorkflowTask, error) {
			task, err := b.Backend.GetWorkflowTask(ctx, queues)
			if err != nil {
				return nil, ctxerrors.Wrap(err, "claim workflow task")
			}

			return task, nil
		})
}

func (b *wakingBackend) GetActivityTask(
	ctx context.Context,
	queues []workflow.Queue,
) (*backend.ActivityTask, error) {
	return claimWhenReady(ctx, b.wake,
		func() (*backend.ActivityTask, error) {
			task, err := b.Backend.GetActivityTask(ctx, queues)
			if err != nil {
				return nil, ctxerrors.Wrap(err, "claim workflow activity")
			}

			return task, nil
		})
}

// claimWhenReady claims once, and when nothing is ready, claims again after
// each write until workflowTaskWait has passed. It answers nil when nothing
// became ready, as an empty claim does, and when the caller stopped waiting.
func claimWhenReady[T any](
	ctx context.Context,
	wake *workflowWake,
	claim func() (*T, error),
) (*T, error) {
	timer := time.NewTimer(workflowTaskWait)
	defer timer.Stop()

	for {
		signal := wake.signal()

		task, err := claimUnlessDone(ctx, claim)
		if err != nil || task != nil || ctx.Err() != nil {
			return task, err
		}

		select {
		case <-signal:
		case <-timer.C:
			return claimUnlessDone(ctx, claim)
		case <-ctx.Done():
			return claimUnlessDone(ctx, claim)
		}
	}
}

// claimUnlessDone answers an empty claim once the caller has stopped
// waiting, including a claim that the cancellation cut off mid-query.
func claimUnlessDone[T any](
	ctx context.Context,
	claim func() (*T, error),
) (*T, error) {
	var (
		task *T
		err  error
	)

	if ctx.Err() == nil {
		task, err = claim()
	}

	if ctx.Err() != nil {
		//nolint:nilnil,nilerr // The backend answers an empty claim this way.
		return nil, nil
	}

	return task, err
}
