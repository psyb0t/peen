package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"gorm.io/gen/field"
	"gorm.io/gorm"

	"github.com/psyb0t/go-workflows/backend"
	"github.com/psyb0t/go-workflows/backend/history"
	"github.com/psyb0t/go-workflows/backend/metadata"
	"github.com/psyb0t/go-workflows/backend/sqlite/db/models"
	"github.com/psyb0t/go-workflows/backend/sqlite/db/repositories"
	"github.com/psyb0t/go-workflows/core"
)

func queueNames(queues []core.Queue) []string {
	names := make([]string, len(queues))
	for i, queue := range queues {
		names[i] = string(queue)
	}
	return names
}

func (s *Store) GetWorkflowTask(ctx context.Context, queues []core.Queue) (*backend.WorkflowTask, error) {
	if len(queues) == 0 {
		return nil, ctxerrors.Wrap(commerr.ErrInvalidArgument, "no queues provided")
	}
	var task *backend.WorkflowTask
	err := s.queries(ctx).Transaction(func(q *repositories.Query) error {
		now := time.Now().UTC()
		repo := q.Instance
		row, err := repo.WithContext(ctx).NextRunnable(now, s.worker, queueNames(queues), int(core.WorkflowInstanceStateActive))
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return ctxerrors.Wrap(err, "select runnable workflow")
		}
		result, err := repo.WithContext(ctx).Where(repo.ID.Eq(row.ID), repo.ExecutionID.Eq(row.ExecutionID)).UpdateSimple(
			repo.LockedUntil.Value(now.Add(s.options.WorkflowLockTimeout)), repo.Worker.Value(s.worker),
		)
		if err != nil {
			return ctxerrors.Wrap(err, "claim workflow")
		}
		if result.RowsAffected != 1 {
			return ctxerrors.Wrap(commerr.ErrConflict, "workflow claim changed")
		}
		instance := instanceFromModel(row)
		events, err := pendingEvents(ctx, q, instance, now)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			return errNoPendingEvents
		}
		var workflowMetadata *metadata.WorkflowMetadata
		if len(row.Metadata) != 0 {
			if err := json.Unmarshal(row.Metadata, &workflowMetadata); err != nil {
				return ctxerrors.Wrap(err, "decode workflow metadata")
			}
		}
		h := q.HistoryEvent
		last, err := h.WithContext(ctx).Where(h.InstanceID.Eq(row.ID), h.ExecutionID.Eq(row.ExecutionID)).Order(h.SequenceID.Desc()).Take()
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return ctxerrors.Wrap(err, "read workflow sequence")
		}
		task = &backend.WorkflowTask{
			ID: row.ID, Queue: core.Queue(row.Queue), WorkflowInstance: instance,
			WorkflowInstanceState: core.WorkflowInstanceState(row.State), Metadata: workflowMetadata, NewEvents: events,
		}
		if last != nil {
			task.LastSequenceID = last.SequenceID
		}
		return nil
	})
	if errors.Is(err, errNoPendingEvents) {
		return nil, nil
	}
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get workflow task")
	}
	return task, nil
}

func (s *Store) ExtendWorkflowTask(ctx context.Context, task *backend.WorkflowTask) error {
	repo := s.queries(ctx).Instance
	result, err := repo.WithContext(ctx).Where(
		repo.ID.Eq(task.WorkflowInstance.InstanceID), repo.ExecutionID.Eq(task.WorkflowInstance.ExecutionID), repo.Worker.Eq(s.worker),
	).UpdateSimple(repo.LockedUntil.Value(time.Now().UTC().Add(s.options.WorkflowLockTimeout)))
	if err != nil {
		return ctxerrors.Wrap(err, "extend workflow claim")
	}
	if result.RowsAffected != 1 {
		return ctxerrors.Wrap(commerr.ErrConflict, "workflow claim not owned")
	}
	return nil
}

func (s *Store) GetActivityTask(ctx context.Context, queues []core.Queue) (*backend.ActivityTask, error) {
	if len(queues) == 0 {
		return nil, ctxerrors.Wrap(commerr.ErrInvalidArgument, "no queues provided")
	}
	var task *backend.ActivityTask
	err := s.queries(ctx).Transaction(func(q *repositories.Query) error {
		now := time.Now().UTC()
		repo := q.Activity
		selection := repo.WithContext(ctx).Where(repo.Queue.In(queueNames(queues)...)).Where(
			repo.WithContext(ctx).Where(repo.LockedUntil.IsNull()).Or(repo.LockedUntil.Lt(now)),
		).Order(repo.RowID)
		row, err := selection.Take()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return ctxerrors.Wrap(err, "select runnable activity")
		}
		result, err := repo.WithContext(ctx).Where(
			repo.ID.Eq(row.ID), repo.InstanceID.Eq(row.InstanceID), repo.ExecutionID.Eq(row.ExecutionID),
		).UpdateSimple(repo.LockedUntil.Value(now.Add(s.options.ActivityLockTimeout)), repo.Worker.Value(s.worker))
		if err != nil {
			return ctxerrors.Wrap(err, "claim activity")
		}
		if result.RowsAffected != 1 {
			return ctxerrors.Wrap(commerr.ErrConflict, "activity claim changed")
		}
		a := q.Attribute
		attributes, err := a.WithContext(ctx).Where(
			a.ID.Eq(row.ID), a.InstanceID.Eq(row.InstanceID), a.ExecutionID.Eq(row.ExecutionID),
		).Take()
		if err != nil {
			return ctxerrors.Wrap(err, "load activity attributes")
		}
		decoded, err := history.DeserializeAttributes(history.EventType(row.EventType), attributes.Data)
		if err != nil {
			return ctxerrors.Wrap(err, "decode activity attributes")
		}
		i := q.Instance
		instance, err := i.WithContext(ctx).Where(i.ID.Eq(row.InstanceID), i.ExecutionID.Eq(row.ExecutionID)).Take()
		if err != nil {
			return ctxerrors.Wrap(err, "load activity workflow")
		}
		var workflowMetadata *metadata.WorkflowMetadata
		if len(instance.Metadata) != 0 {
			if err := json.Unmarshal(instance.Metadata, &workflowMetadata); err != nil {
				return ctxerrors.Wrap(err, "decode activity workflow metadata")
			}
		}
		task = &backend.ActivityTask{
			ID: row.ID, ActivityID: row.ID, Queue: core.Queue(row.Queue),
			WorkflowInstance: instanceFromModel(instance),
			Event: &history.Event{
				ID: row.ID, Type: history.EventType(row.EventType), Timestamp: row.Timestamp,
				ScheduleEventID: row.ScheduleEventID, VisibleAt: row.VisibleAt, Attributes: decoded,
			},
		}
		return nil
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get activity task")
	}
	return task, nil
}

func (s *Store) ExtendActivityTask(ctx context.Context, task *backend.ActivityTask) error {
	repo := s.queries(ctx).Activity
	result, err := repo.WithContext(ctx).Where(
		repo.ID.Eq(task.ActivityID), repo.InstanceID.Eq(task.WorkflowInstance.InstanceID),
		repo.ExecutionID.Eq(task.WorkflowInstance.ExecutionID), repo.Worker.Eq(s.worker),
	).UpdateSimple(repo.LockedUntil.Value(time.Now().UTC().Add(s.options.ActivityLockTimeout)))
	if err != nil {
		return ctxerrors.Wrap(err, "extend activity claim")
	}
	if result.RowsAffected != 1 {
		return ctxerrors.Wrap(commerr.ErrConflict, "activity claim not owned")
	}
	return nil
}

func (s *Store) CompleteActivityTask(ctx context.Context, task *backend.ActivityTask, result *history.Event) error {
	err := s.queries(ctx).Transaction(func(q *repositories.Query) error {
		repo := q.Activity
		deleted, err := repo.WithContext(ctx).Where(
			repo.ID.Eq(task.ActivityID), repo.InstanceID.Eq(task.WorkflowInstance.InstanceID),
			repo.ExecutionID.Eq(task.WorkflowInstance.ExecutionID), repo.Worker.Eq(s.worker), repo.Queue.Eq(string(task.Queue)),
		).Delete()
		if err != nil {
			return ctxerrors.Wrap(err, "remove completed activity")
		}
		if deleted.RowsAffected != 1 {
			return ctxerrors.Wrap(commerr.ErrConflict, "activity claim not owned")
		}
		return insertEvents(ctx, q, task.WorkflowInstance, []*history.Event{result}, false)
	})
	if err != nil {
		return ctxerrors.Wrap(err, "complete activity task")
	}
	return nil
}

func (s *Store) CompleteWorkflowTask(
	ctx context.Context, task *backend.WorkflowTask, state core.WorkflowInstanceState,
	executed, activities, timers []*history.Event, workflowEvents []*history.WorkflowEvent,
) error {
	err := s.queries(ctx).Transaction(func(q *repositories.Query) error {
		repo := q.Instance
		assignments := []field.AssignExpr{
			repo.LockedUntil.Null(), repo.StickyUntil.Value(time.Now().UTC().Add(s.options.StickyTimeout)), repo.State.Value(int(state)),
		}
		if state == core.WorkflowInstanceStateContinuedAsNew || state == core.WorkflowInstanceStateFinished {
			assignments = append(assignments, repo.CompletedAt.Value(time.Now().UTC()))
		} else {
			assignments = append(assignments, repo.CompletedAt.Null())
		}
		changed, err := repo.WithContext(ctx).Where(
			repo.ID.Eq(task.WorkflowInstance.InstanceID), repo.ExecutionID.Eq(task.WorkflowInstance.ExecutionID), repo.Worker.Eq(s.worker), repo.LockedUntil.IsNotNull(),
		).UpdateSimple(assignments...)
		if err != nil {
			return ctxerrors.Wrap(err, "checkpoint workflow instance")
		}
		if changed.RowsAffected != 1 {
			return ctxerrors.Wrap(commerr.ErrConflict, "workflow claim not owned")
		}
		if len(executed) != 0 {
			ids := make([]string, len(executed))
			for index, event := range executed {
				ids[index] = event.ID
			}
			p := q.PendingEvent
			if _, err := p.WithContext(ctx).Where(
				p.InstanceID.Eq(task.WorkflowInstance.InstanceID), p.ExecutionID.Eq(task.WorkflowInstance.ExecutionID), p.ID.In(ids...),
			).Delete(); err != nil {
				return ctxerrors.Wrap(err, "remove handled pending events")
			}
		}
		if err := insertEvents(ctx, q, task.WorkflowInstance, executed, true); err != nil {
			return err
		}
		for _, event := range activities {
			attributes, ok := event.Attributes.(*history.ActivityScheduledAttributes)
			if !ok {
				return ctxerrors.Wrap(commerr.ErrInvalidArgument, "activity schedule attributes")
			}
			queue := attributes.Queue
			if queue == "" {
				queue = task.Queue
			}
			a := q.Activity
			if err := a.WithContext(ctx).Omit(a.RowID).Create(&models.Activity{
				ID: event.ID, InstanceID: task.WorkflowInstance.InstanceID, ExecutionID: task.WorkflowInstance.ExecutionID,
				EventType: uint(event.Type), Queue: string(queue), Timestamp: event.Timestamp,
				ScheduleEventID: event.ScheduleEventID, VisibleAt: event.VisibleAt,
			}); err != nil {
				return ctxerrors.Wrap(err, "schedule activity")
			}
		}
		if err := insertEvents(ctx, q, task.WorkflowInstance, timers, false); err != nil {
			return err
		}
		for _, event := range executed {
			if event.Type != history.EventType_TimerCanceled {
				continue
			}
			if err := removeFutureEvent(ctx, q, task.WorkflowInstance, event.ScheduleEventID); err != nil {
				return err
			}
		}
		if err := s.scheduleWorkflows(ctx, q, task, workflowEvents); err != nil {
			return err
		}
		if s.options.RemoveContinuedAsNewInstances && state == core.WorkflowInstanceStateContinuedAsNew {
			return removeInstance(ctx, q, task.WorkflowInstance)
		}
		return nil
	})
	if err != nil {
		return ctxerrors.Wrap(err, "complete workflow task")
	}
	return nil
}
