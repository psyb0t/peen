package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/psyb0t/ctxerrors"

	"github.com/benbjohnson/clock"

	"github.com/psyb0t/go-workflows/backend"
	"github.com/psyb0t/go-workflows/backend/history"
	"github.com/psyb0t/go-workflows/backend/metrics"
	"github.com/psyb0t/go-workflows/backend/payload"
	"github.com/psyb0t/go-workflows/internal/activity"
	"github.com/psyb0t/go-workflows/internal/metrickeys"
	im "github.com/psyb0t/go-workflows/internal/metrics"
	"github.com/psyb0t/go-workflows/internal/workflowerrors"
	"github.com/psyb0t/go-workflows/registry"
	"github.com/psyb0t/go-workflows/workflow"
)

func NewActivityWorker(
	b backend.Backend,
	registry *registry.Registry,
	clock clock.Clock,
	options WorkerOptions,
) *Worker[backend.ActivityTask, history.Event] {
	ae := activity.NewExecutor(b.Options().Logger, b.Tracer(), b.Options().Converter, b.Options().ContextPropagators, registry)

	tw := &ActivityTaskWorker{
		backend:              b,
		activityTaskExecutor: ae,
		clock:                clock,
		logger:               b.Options().Logger,
	}

	return NewWorker(b, tw, &options)
}

type ActivityTaskWorker struct {
	backend              backend.Backend
	activityTaskExecutor *activity.Executor
	clock                clock.Clock
	logger               *slog.Logger
}

func (atw *ActivityTaskWorker) Complete(ctx context.Context, result *history.Event, task *backend.ActivityTask) error {
	if err := atw.backend.CompleteActivityTask(ctx, task, result); err != nil {
		atw.backend.Options().Logger.Error("completing activity task", "error", err)
	}

	return nil
}

func (atw *ActivityTaskWorker) Start(ctx context.Context, queues []workflow.Queue) error {
	if err := atw.backend.PrepareActivityQueues(ctx, queues); err != nil {
		return ctxerrors.Wrap(err, "preparing activity queues")
	}

	return nil
}

func (atw *ActivityTaskWorker) Execute(ctx context.Context, task *backend.ActivityTask) (*history.Event, error) {
	a := task.Event.Attributes.(*history.ActivityScheduledAttributes)
	ametrics := atw.backend.Metrics().WithTags(metrics.Tags{metrickeys.ActivityName: a.Name})

	// Record how long this task was in the queue
	scheduledAt := task.Event.Timestamp
	timeInQueue := time.Since(scheduledAt)
	ametrics.Distribution(metrickeys.ActivityTaskDelay, metrics.Tags{}, float64(timeInQueue/time.Millisecond))

	timer := im.NewTimer(ametrics, metrickeys.ActivityTaskProcessed, metrics.Tags{})
	defer timer.Stop()

	result, err := atw.activityTaskExecutor.ExecuteActivity(ctx, task)
	event := atw.resultToEvent(task.Event.ScheduleEventID, result, err)

	return event, nil
}

func (atw *ActivityTaskWorker) Extend(ctx context.Context, task *backend.ActivityTask) error {
	return atw.backend.ExtendActivityTask(ctx, task)
}

func (atw *ActivityTaskWorker) Get(ctx context.Context, queues []workflow.Queue) (*backend.ActivityTask, error) {
	return atw.backend.GetActivityTask(ctx, queues)
}

func (atw *ActivityTaskWorker) resultToEvent(scheduleEventID int64, result payload.Payload, err error) *history.Event {
	if err != nil {
		return history.NewPendingEvent(
			atw.clock.Now(),
			history.EventType_ActivityFailed,
			&history.ActivityFailedAttributes{
				Error: workflowerrors.FromError(err),
			},
			history.ScheduleEventID(scheduleEventID),
		)
	}

	return history.NewPendingEvent(
		atw.clock.Now(),
		history.EventType_ActivityCompleted,
		&history.ActivityCompletedAttributes{
			Result: result,
		},
		history.ScheduleEventID(scheduleEventID))
}
