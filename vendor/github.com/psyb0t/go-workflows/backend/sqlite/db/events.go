package db

import (
	"context"
	"time"

	"github.com/psyb0t/ctxerrors"
	"gorm.io/gorm/clause"

	"github.com/psyb0t/go-workflows/backend/history"
	"github.com/psyb0t/go-workflows/backend/sqlite/db/models"
	"github.com/psyb0t/go-workflows/backend/sqlite/db/repositories"
	"github.com/psyb0t/go-workflows/core"
)

const eventBatchSize = 20

func eventsFromModels(rows []*models.EventPayload) ([]*history.Event, error) {
	events := make([]*history.Event, 0, len(rows))
	for _, row := range rows {
		attributes, err := history.DeserializeAttributes(history.EventType(row.EventType), row.Data)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "decode persisted event attributes")
		}
		events = append(events, &history.Event{
			ID: row.ID, SequenceID: row.SequenceID, Type: history.EventType(row.EventType),
			Timestamp: row.Timestamp, ScheduleEventID: row.ScheduleEventID,
			VisibleAt: row.VisibleAt, Attributes: attributes,
		})
	}
	return events, nil
}

func pendingEvents(ctx context.Context, q *repositories.Query, instance *core.WorkflowInstance, now time.Time) ([]*history.Event, error) {
	rows, err := q.PendingEvent.WithContext(ctx).EventsWithAttributes(instance.InstanceID, instance.ExecutionID, -1, true, now)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "load pending events")
	}
	return eventsFromModels(rows)
}

func (s *Store) GetWorkflowInstanceHistory(ctx context.Context, instance *core.WorkflowInstance, after *int64) ([]*history.Event, error) {
	afterSequence := int64(-1)
	if after != nil {
		afterSequence = *after
	}
	rows, err := s.queries(ctx).HistoryEvent.WithContext(ctx).EventsWithAttributes(instance.InstanceID, instance.ExecutionID, afterSequence, false, time.Now().UTC())
	if err != nil {
		return nil, ctxerrors.Wrap(err, "load workflow history")
	}
	return eventsFromModels(rows)
}

func (s *Store) GetFutureEvents(ctx context.Context) ([]*history.Event, error) {
	rows, err := s.queries(ctx).PendingEvent.WithContext(ctx).FutureWithAttributes()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "load future events")
	}
	return eventsFromModels(rows)
}

func insertEvents(ctx context.Context, q *repositories.Query, instance *core.WorkflowInstance, events []*history.Event, executed bool) error {
	if len(events) == 0 {
		return nil
	}
	attributes := make([]*models.Attribute, 0, len(events))
	pending := make([]*models.PendingEvent, 0, len(events))
	past := make([]*models.HistoryEvent, 0, len(events))
	for _, event := range events {
		data, err := history.SerializeAttributes(event.Attributes)
		if err != nil {
			return ctxerrors.Wrap(err, "encode event attributes")
		}
		attributes = append(attributes, &models.Attribute{
			ID: event.ID, InstanceID: instance.InstanceID, ExecutionID: instance.ExecutionID, Data: data,
		})
		if executed {
			past = append(past, &models.HistoryEvent{
				ID: event.ID, InstanceID: instance.InstanceID, ExecutionID: instance.ExecutionID,
				SequenceID: event.SequenceID, EventType: uint(event.Type), Timestamp: event.Timestamp,
				ScheduleEventID: event.ScheduleEventID, VisibleAt: event.VisibleAt,
			})
			continue
		}
		pending = append(pending, &models.PendingEvent{
			ID: event.ID, InstanceID: instance.InstanceID, ExecutionID: instance.ExecutionID,
			SequenceID: event.SequenceID, EventType: uint(event.Type), Timestamp: event.Timestamp,
			ScheduleEventID: event.ScheduleEventID, VisibleAt: event.VisibleAt,
		})
	}
	a := q.Attribute
	if err := a.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: a.ID.ColumnName().String()}, {Name: a.InstanceID.ColumnName().String()}, {Name: a.ExecutionID.ColumnName().String()},
		},
		DoNothing: true,
	}).CreateInBatches(attributes, eventBatchSize); err != nil {
		return ctxerrors.Wrap(err, "insert event attributes")
	}
	if executed {
		repo := q.HistoryEvent
		if err := repo.WithContext(ctx).Omit(repo.RowID).CreateInBatches(past, eventBatchSize); err != nil {
			return ctxerrors.Wrap(err, "insert history events")
		}
		return nil
	}
	repo := q.PendingEvent
	if err := repo.WithContext(ctx).Omit(repo.RowID).CreateInBatches(pending, eventBatchSize); err != nil {
		return ctxerrors.Wrap(err, "insert pending events")
	}
	return nil
}

func removeFutureEvent(ctx context.Context, q *repositories.Query, instance *core.WorkflowInstance, scheduleID int64) error {
	repo := q.PendingEvent
	rows, err := repo.WithContext(ctx).Where(
		repo.InstanceID.Eq(instance.InstanceID), repo.ExecutionID.Eq(instance.ExecutionID),
		repo.ScheduleEventID.Eq(scheduleID), repo.VisibleAt.IsNotNull(),
	).Find()
	if err != nil {
		return ctxerrors.Wrap(err, "find canceled timer events")
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	if _, err := repo.WithContext(ctx).Where(
		repo.InstanceID.Eq(instance.InstanceID), repo.ExecutionID.Eq(instance.ExecutionID), repo.ID.In(ids...),
	).Delete(); err != nil {
		return ctxerrors.Wrap(err, "delete canceled timer events")
	}
	a := q.Attribute
	if _, err := a.WithContext(ctx).Where(
		a.InstanceID.Eq(instance.InstanceID), a.ExecutionID.Eq(instance.ExecutionID), a.ID.In(ids...),
	).Delete(); err != nil {
		return ctxerrors.Wrap(err, "delete canceled timer attributes")
	}
	return nil
}
