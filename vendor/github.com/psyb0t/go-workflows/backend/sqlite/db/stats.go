package db

import (
	"context"
	"errors"
	"time"

	"github.com/psyb0t/ctxerrors"

	"github.com/psyb0t/go-workflows/backend"
	"github.com/psyb0t/go-workflows/backend/history"
	"github.com/psyb0t/go-workflows/backend/sqlite/db/models"
	"github.com/psyb0t/go-workflows/backend/sqlite/db/repositories"
	"github.com/psyb0t/go-workflows/core"
	"github.com/psyb0t/go-workflows/internal/workflowerrors"
)

const countAlias = "count"

func (s *Store) GetStats(ctx context.Context) (*backend.Stats, error) {
	stats := &backend.Stats{
		PendingWorkflowTasks: map[core.Queue]int64{},
		PendingActivityTasks: map[core.Queue]int64{},
	}
	err := s.queries(ctx).Transaction(func(q *repositories.Query) error {
		i := q.Instance
		count, err := i.WithContext(ctx).Where(i.CompletedAt.IsNull()).Count()
		if err != nil {
			return ctxerrors.Wrap(err, "count active workflow instances")
		}
		stats.ActiveWorkflowInstances = count
		workflows, err := i.WithContext(ctx).RunnableCounts(int(core.WorkflowInstanceStateActive), time.Now().UTC())
		if err != nil {
			return ctxerrors.Wrap(err, "count runnable workflows")
		}
		for _, row := range workflows {
			stats.PendingWorkflowTasks[core.Queue(row.Queue)] = row.Count
		}
		a := q.Activity
		var activities []*models.QueueCount
		if err := a.WithContext(ctx).Select(a.Queue, a.ID.Count().As(countAlias)).Group(a.Queue).Scan(&activities); err != nil {
			return ctxerrors.Wrap(err, "count activities")
		}
		for _, row := range activities {
			stats.PendingActivityTasks[core.Queue(row.Queue)] = row.Count
		}
		return nil
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get backend stats")
	}
	return stats, nil
}

func (s *Store) scheduleWorkflows(ctx context.Context, q *repositories.Query, task *backend.WorkflowTask, events []*history.WorkflowEvent) error {
	for target, messages := range history.EventsByWorkflowInstance(events) {
		first := messages[0]
		if first.HistoryEvent.Type == history.EventType_WorkflowExecutionStarted {
			attributes, ok := first.HistoryEvent.Attributes.(*history.ExecutionStartedAttributes)
			if !ok {
				return ctxerrors.New("invalid workflow start attributes")
			}
			queue := attributes.Queue
			if queue == "" {
				queue = task.Queue
			}
			if err := createInstance(ctx, q, queue, first.WorkflowInstance, attributes.Metadata); err != nil {
				if !errors.Is(err, backend.ErrInstanceAlreadyExists) {
					return ctxerrors.Wrap(err, "create scheduled workflow")
				}
				failure := history.NewPendingEvent(
					time.Now().UTC(), history.EventType_SubWorkflowFailed,
					&history.SubWorkflowFailedAttributes{Error: workflowerrors.FromError(backend.ErrInstanceAlreadyExists)},
					history.ScheduleEventID(first.WorkflowInstance.ParentEventID),
				)
				if err := insertEvents(ctx, q, task.WorkflowInstance, []*history.Event{failure}, false); err != nil {
					return err
				}
				continue
			}
		}
		pending := make([]*history.Event, 0, len(messages))
		for _, message := range messages {
			pending = append(pending, message.HistoryEvent)
		}
		if err := insertEvents(ctx, q, &target, pending, false); err != nil {
			return err
		}
	}
	return nil
}
