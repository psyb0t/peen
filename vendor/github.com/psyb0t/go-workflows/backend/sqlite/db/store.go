package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"

	"github.com/psyb0t/go-workflows/backend"
	"github.com/psyb0t/go-workflows/backend/history"
	"github.com/psyb0t/go-workflows/backend/metadata"
	"github.com/psyb0t/go-workflows/backend/metrics"
	"github.com/psyb0t/go-workflows/backend/sqlite/db/models"
	"github.com/psyb0t/go-workflows/backend/sqlite/db/repositories"
	"github.com/psyb0t/go-workflows/core"
	"github.com/psyb0t/go-workflows/diag"
	"github.com/psyb0t/go-workflows/internal/metrickeys"
)

// Store binds generated queries to one worker's connection, never a global query.
type Store struct {
	database *gorm.DB
	worker   string
	options  *backend.Options
}

func New(pool *sql.DB, worker string, options *backend.Options) (*Store, error) {
	database, err := Open(pool)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "initialize backend database")
	}
	return &Store{database: database, worker: worker, options: options}, nil
}

func (s *Store) queries(ctx context.Context) *repositories.Query {
	return repositories.Use(s.database.WithContext(ctx))
}

func (s *Store) Options() *backend.Options { return s.options }

// Close leaves the pool lifecycle with the backend constructor.
func (s *Store) Close() error { return nil }

func (s *Store) Metrics() metrics.Client {
	return s.options.Metrics.WithTags(metrics.Tags{metrickeys.Backend: "sqlite"})
}
func (s *Store) Tracer() trace.Tracer                                      { return s.options.TracerProvider.Tracer(backend.TracerName) }
func (s *Store) FeatureSupported(backend.Feature) bool                     { return true }
func (s *Store) PrepareWorkflowQueues(context.Context, []core.Queue) error { return nil }
func (s *Store) PrepareActivityQueues(context.Context, []core.Queue) error { return nil }

func (s *Store) CreateWorkflowInstance(ctx context.Context, instance *core.WorkflowInstance, event *history.Event) error {
	attributes, ok := event.Attributes.(*history.ExecutionStartedAttributes)
	if !ok {
		return ctxerrors.Wrap(commerr.ErrInvalidArgument, "workflow start attributes")
	}
	err := s.queries(ctx).Transaction(func(q *repositories.Query) error {
		if err := createInstance(ctx, q, attributes.Queue, instance, attributes.Metadata); err != nil {
			return err
		}
		return insertEvents(ctx, q, instance, []*history.Event{event}, false)
	})
	if err != nil {
		return ctxerrors.Wrap(err, "create workflow instance")
	}
	return nil
}

func createInstance(ctx context.Context, q *repositories.Query, queue core.Queue, instance *core.WorkflowInstance, metadata *metadata.WorkflowMetadata) error {
	repo := q.Instance
	count, err := repo.WithContext(ctx).Where(repo.ID.Eq(instance.InstanceID), repo.State.Eq(int(core.WorkflowInstanceStateActive))).Count()
	if err != nil {
		return ctxerrors.Wrap(err, "check active workflow instance")
	}
	if count != 0 {
		return backend.ErrInstanceAlreadyExists
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return ctxerrors.Wrap(err, "encode workflow metadata")
	}
	row := &models.Instance{
		ID: instance.InstanceID, ExecutionID: instance.ExecutionID, Queue: string(queue),
		State: int(core.WorkflowInstanceStateActive), Metadata: data, CreatedAt: time.Now().UTC(),
	}
	if instance.SubWorkflow() {
		row.ParentInstanceID = &instance.Parent.InstanceID
		row.ParentExecutionID = &instance.Parent.ExecutionID
		row.ParentScheduleEventID = &instance.ParentEventID
	}
	if err := repo.WithContext(ctx).Create(row); err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return ctxerrors.Wrap(backend.ErrInstanceAlreadyExists, "create workflow instance")
		}
		return ctxerrors.Wrap(err, "insert workflow instance")
	}
	return nil
}

func (s *Store) CancelWorkflowInstance(ctx context.Context, instance *core.WorkflowInstance, event *history.Event) error {
	err := s.queries(ctx).Transaction(func(q *repositories.Query) error {
		repo := q.Instance
		_, err := repo.WithContext(ctx).Where(repo.ID.Eq(instance.InstanceID), repo.ExecutionID.Eq(instance.ExecutionID)).Take()
		if err != nil {
			return instanceError(err)
		}
		return insertEvents(ctx, q, instance, []*history.Event{event}, false)
	})
	if err != nil {
		return ctxerrors.Wrap(err, "cancel workflow instance")
	}
	return nil
}

func (s *Store) SignalWorkflow(ctx context.Context, instanceID string, event *history.Event) error {
	err := s.queries(ctx).Transaction(func(q *repositories.Query) error {
		repo := q.Instance
		row, err := repo.WithContext(ctx).Where(repo.ID.Eq(instanceID), repo.State.Eq(int(core.WorkflowInstanceStateActive))).Take()
		if err != nil {
			return instanceError(err)
		}
		return insertEvents(ctx, q, instanceFromModel(row), []*history.Event{event}, false)
	})
	if err != nil {
		return ctxerrors.Wrap(err, "signal workflow instance")
	}
	return nil
}

func (s *Store) GetWorkflowInstanceState(ctx context.Context, instance *core.WorkflowInstance) (core.WorkflowInstanceState, error) {
	repo := s.queries(ctx).Instance
	row, err := repo.WithContext(ctx).Where(repo.ID.Eq(instance.InstanceID), repo.ExecutionID.Eq(instance.ExecutionID)).Take()
	if err != nil {
		return core.WorkflowInstanceStateActive, instanceError(err)
	}
	return core.WorkflowInstanceState(row.State), nil
}

func instanceError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ctxerrors.Wrap(backend.ErrInstanceNotFound, "find workflow instance")
	}
	return ctxerrors.Wrap(err, "query workflow instance")
}

func (s *Store) RemoveWorkflowInstance(ctx context.Context, instance *core.WorkflowInstance) error {
	err := s.queries(ctx).Transaction(func(q *repositories.Query) error { return removeInstance(ctx, q, instance) })
	if err != nil {
		return ctxerrors.Wrap(err, "remove workflow instance")
	}
	return nil
}

func removeInstance(ctx context.Context, q *repositories.Query, instance *core.WorkflowInstance) error {
	repo := q.Instance
	row, err := repo.WithContext(ctx).Where(repo.ID.Eq(instance.InstanceID), repo.ExecutionID.Eq(instance.ExecutionID)).Take()
	if err != nil {
		return instanceError(err)
	}
	if core.WorkflowInstanceState(row.State) == core.WorkflowInstanceStateActive {
		return backend.ErrInstanceNotFinished
	}
	if _, err := repo.WithContext(ctx).Where(repo.ID.Eq(instance.InstanceID), repo.ExecutionID.Eq(instance.ExecutionID)).Delete(); err != nil {
		return ctxerrors.Wrap(err, "delete workflow instance")
	}
	h, a, p, activity := q.HistoryEvent, q.Attribute, q.PendingEvent, q.Activity
	if _, err := h.WithContext(ctx).Where(h.InstanceID.Eq(instance.InstanceID), h.ExecutionID.Eq(instance.ExecutionID)).Delete(); err != nil {
		return ctxerrors.Wrap(err, "delete workflow history")
	}
	if _, err := a.WithContext(ctx).Where(a.InstanceID.Eq(instance.InstanceID), a.ExecutionID.Eq(instance.ExecutionID)).Delete(); err != nil {
		return ctxerrors.Wrap(err, "delete workflow attributes")
	}
	if _, err := p.WithContext(ctx).Where(p.InstanceID.Eq(instance.InstanceID), p.ExecutionID.Eq(instance.ExecutionID)).Delete(); err != nil {
		return ctxerrors.Wrap(err, "delete workflow pending events")
	}
	if _, err := activity.WithContext(ctx).Where(activity.InstanceID.Eq(instance.InstanceID), activity.ExecutionID.Eq(instance.ExecutionID)).Delete(); err != nil {
		return ctxerrors.Wrap(err, "delete workflow activities")
	}
	return nil
}

func (s *Store) RemoveWorkflowInstances(ctx context.Context, options ...backend.RemovalOption) error {
	settings := backend.DefaultRemovalOptions
	for _, option := range options {
		option(&settings)
	}
	if settings.BatchSize <= 0 {
		return ctxerrors.Wrap(commerr.ErrInvalidArgument, "removal batch size must be positive")
	}
	for {
		removed := 0
		err := s.queries(ctx).Transaction(func(q *repositories.Query) error {
			repo := q.Instance
			rows, err := repo.WithContext(ctx).Where(repo.CompletedAt.Lt(settings.FinishedBefore)).Limit(settings.BatchSize).Find()
			if err != nil {
				return ctxerrors.Wrap(err, "list expired instances")
			}
			for _, row := range rows {
				if err := removeInstance(ctx, q, instanceFromModel(row)); err != nil {
					return err
				}
				removed++
			}
			return nil
		})
		if err != nil {
			return ctxerrors.Wrap(err, "remove expired instances")
		}
		if removed < settings.BatchSize {
			return nil
		}
	}
}

func instanceFromModel(row *models.Instance) *core.WorkflowInstance {
	if row.ParentInstanceID != nil && row.ParentExecutionID != nil && row.ParentScheduleEventID != nil {
		return core.NewSubWorkflowInstance(row.ID, row.ExecutionID, core.NewWorkflowInstance(*row.ParentInstanceID, *row.ParentExecutionID), *row.ParentScheduleEventID)
	}
	return core.NewWorkflowInstance(row.ID, row.ExecutionID)
}

func referenceFromModel(row *models.Instance) *diag.WorkflowInstanceRef {
	return &diag.WorkflowInstanceRef{
		Instance: instanceFromModel(row), CreatedAt: row.CreatedAt, CompletedAt: row.CompletedAt,
		State: core.WorkflowInstanceState(row.State), Queue: row.Queue,
	}
}

func (s *Store) GetWorkflowInstances(ctx context.Context, afterID, afterExecutionID string, count int) ([]*diag.WorkflowInstanceRef, error) {
	if count <= 0 {
		return nil, ctxerrors.Wrap(commerr.ErrInvalidArgument, "page size must be positive")
	}
	repo := s.queries(ctx).Instance
	var rows []*models.Instance
	var err error
	if afterID == "" {
		rows, err = repo.WithContext(ctx).Order(repo.CreatedAt.Desc(), repo.ID.Desc(), repo.ExecutionID.Desc()).Limit(count).Find()
	} else {
		rows, err = repo.WithContext(ctx).PageAfter(afterID, afterExecutionID, count)
	}
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list workflow instances")
	}
	refs := make([]*diag.WorkflowInstanceRef, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, referenceFromModel(row))
	}
	return refs, nil
}

func (s *Store) GetWorkflowInstance(ctx context.Context, instance *core.WorkflowInstance) (*diag.WorkflowInstanceRef, error) {
	repo := s.queries(ctx).Instance
	row, err := repo.WithContext(ctx).Where(repo.ID.Eq(instance.InstanceID), repo.ExecutionID.Eq(instance.ExecutionID)).Take()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get workflow instance")
	}
	return referenceFromModel(row), nil
}

func (s *Store) GetWorkflowTree(ctx context.Context, instance *core.WorkflowInstance) (*diag.WorkflowInstanceTree, error) {
	return diag.NewInstanceTreeBuilder(s).BuildWorkflowInstanceTree(ctx, instance)
}
