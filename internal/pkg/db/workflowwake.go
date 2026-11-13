package db

import (
	"context"
	"slices"
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
	pending map[chan struct{}][]workflow.Queue
}

func newWorkflowWake() *workflowWake {
	return &workflowWake{pending: make(map[chan struct{}][]workflow.Queue)}
}

// Subscribe before claiming so a commit between the claim and wait is kept.
// Each subscription is retired when its bounded claim call returns.
func (w *workflowWake) subscribe(
	queues []workflow.Queue,
) (chan struct{}, func()) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	signal := make(chan struct{}, 1)
	w.pending[signal] = slices.Clone(queues)

	return signal, func() {
		w.mutex.Lock()
		defer w.mutex.Unlock()

		delete(w.pending, signal)
	}
}

// An unknown target queue broadcasts, preserving signal and cancellation
// behavior. Ordinary task completions wake only their queue's claim loops.
func (w *workflowWake) notify(queues ...workflow.Queue) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	for signal, subscribed := range w.pending {
		matches := len(queues) == 0
		for _, queue := range queues {
			matches = matches || slices.Contains(subscribed, queue)
		}

		if !matches {
			continue
		}

		select {
		case signal <- struct{}{}:
		default: // A pending notification already guarantees another claim.
		}
	}
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
	err := b.Backend.CreateWorkflowInstance(ctx, instance, event)
	if err != nil {
		return ctxerrors.Wrap(err, "create workflow instance")
	}

	value := event.Attributes

	attributes, ok := value.(*history.ExecutionStartedAttributes)
	if !ok || attributes.Queue == "" {
		b.wake.notify()

		return nil
	}

	b.wake.notify(attributes.Queue)

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
	err := b.Backend.CompleteWorkflowTask(ctx, task, state,
		executed, activities, timers, targets)
	if err != nil {
		return ctxerrors.Wrap(err, "complete workflow task")
	}

	b.notifyWorkflowCompletion(task, activities, targets)

	return nil
}

func (b *wakingBackend) CompleteActivityTask(
	ctx context.Context,
	task *backend.ActivityTask,
	result *history.Event,
) error {
	err := b.Backend.CompleteActivityTask(ctx, task, result)
	if err != nil {
		return ctxerrors.Wrap(err, "complete workflow activity")
	}

	// The activity queue can differ from its workflow's queue, which the
	// activity task does not carry.
	b.wake.notify()

	return nil
}

func (b *wakingBackend) notifyWorkflowCompletion(
	task *backend.WorkflowTask,
	activities []*history.Event,
	targets []*history.WorkflowEvent,
) {
	if len(targets) > 0 {
		// An existing target's queue is not carried in a workflow event.
		b.wake.notify()

		return
	}

	queues := []workflow.Queue{task.Queue}

	for _, event := range activities {
		value := event.Attributes

		attributes, ok := value.(*history.ActivityScheduledAttributes)
		if ok && attributes.Queue != "" {
			queues = append(queues, attributes.Queue)
		}
	}

	b.wake.notify(queues...)
}

func (b *wakingBackend) GetWorkflowTask(
	ctx context.Context,
	queues []workflow.Queue,
) (*backend.WorkflowTask, error) {
	return claimWhenReady(ctx, b.wake, queues,
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
	return claimWhenReady(ctx, b.wake, queues,
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
	queues []workflow.Queue,
	claim func() (*T, error),
) (*T, error) {
	timer := time.NewTimer(workflowTaskWait)
	defer timer.Stop()

	signal, unsubscribe := wake.subscribe(queues)
	defer unsubscribe()

	for {
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
