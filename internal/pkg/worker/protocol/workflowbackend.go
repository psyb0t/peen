package protocol

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/cschleiden/go-workflows/backend"
	"github.com/cschleiden/go-workflows/backend/history"
	"github.com/cschleiden/go-workflows/backend/metrics"
	"github.com/cschleiden/go-workflows/core"
	"github.com/cschleiden/go-workflows/workflow"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"go.opentelemetry.io/otel/trace"
)

// WorkflowBackend runs the native workflow worker over its authenticated
// controller connection. It never opens or closes controller storage.
type WorkflowBackend struct {
	conn      *Conn
	sessionID uuid.UUID
	options   *backend.Options
}

var _ backend.Backend = (*WorkflowBackend)(nil)

func NewWorkflowBackend(conn *Conn, sessionID uuid.UUID) *WorkflowBackend {
	return &WorkflowBackend{
		conn: conn, sessionID: sessionID, options: backend.ApplyOptions(),
	}
}

func (b *WorkflowBackend) CreateWorkflowInstance(
	ctx context.Context,
	instance *workflow.Instance,
	event *history.Event,
) error {
	return b.write(ctx, workflowRequest{
		Operation: workflowCreateInstance, Instance: instance,
		Event: &workflowEvent{Event: event},
	})
}

func (b *WorkflowBackend) CancelWorkflowInstance(
	ctx context.Context,
	instance *workflow.Instance,
	event *history.Event,
) error {
	return b.write(ctx, workflowRequest{
		Operation: workflowCancelInstance, Instance: instance,
		Event: &workflowEvent{Event: event},
	})
}

func (b *WorkflowBackend) RemoveWorkflowInstance(
	_ context.Context,
	_ *workflow.Instance,
) error {
	return ctxerrors.Wrap(commerr.ErrPermissionDenied,
		"workflow history retention belongs to the controller")
}

func (b *WorkflowBackend) RemoveWorkflowInstances(
	_ context.Context,
	_ ...backend.RemovalOption,
) error {
	return ctxerrors.Wrap(commerr.ErrPermissionDenied,
		"workflow history retention belongs to the controller")
}

func (b *WorkflowBackend) GetWorkflowInstanceState(
	ctx context.Context,
	instance *workflow.Instance,
) (core.WorkflowInstanceState, error) {
	return workflowCall[core.WorkflowInstanceState](ctx, b, workflowRequest{
		Operation: workflowGetState, Instance: instance,
	})
}

func (b *WorkflowBackend) GetWorkflowInstanceHistory(
	ctx context.Context,
	instance *workflow.Instance,
	lastSequenceID *int64,
) ([]*history.Event, error) {
	events, err := workflowCall[[]*workflowEvent](ctx, b, workflowRequest{
		Operation: workflowGetHistory, Instance: instance,
		LastSequenceID: lastSequenceID,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read remote workflow history")
	}

	return decodeWorkflowEvents(events), nil
}

func (b *WorkflowBackend) SignalWorkflow(
	ctx context.Context,
	instanceID string,
	event *history.Event,
) error {
	return b.write(ctx, workflowRequest{
		Operation: workflowSignal, InstanceID: instanceID,
		Event: &workflowEvent{Event: event},
	})
}

func (b *WorkflowBackend) PrepareWorkflowQueues(
	ctx context.Context,
	queues []workflow.Queue,
) error {
	return b.prepareQueues(ctx, queues)
}

func (b *WorkflowBackend) PrepareActivityQueues(
	ctx context.Context,
	queues []workflow.Queue,
) error {
	return b.prepareQueues(ctx, queues)
}

func (b *WorkflowBackend) prepareQueues(
	ctx context.Context,
	queues []workflow.Queue,
) error {
	return b.write(ctx, workflowRequest{
		Operation: workflowPrepareQueues, Queues: queues,
	})
}

func (b *WorkflowBackend) GetWorkflowTask(
	ctx context.Context,
	queues []workflow.Queue,
) (*backend.WorkflowTask, error) {
	task, err := workflowCall[*workflowTask](ctx, b, workflowRequest{
		Operation: workflowGetTask, Queues: queues,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get remote workflow task")
	}

	return task.native(), nil
}

func (b *WorkflowBackend) ExtendWorkflowTask(
	ctx context.Context,
	task *backend.WorkflowTask,
) error {
	return b.write(ctx, workflowRequest{
		Operation: workflowExtendTask, Task: encodeWorkflowTask(task),
	})
}

func (b *WorkflowBackend) CompleteWorkflowTask(
	ctx context.Context,
	task *backend.WorkflowTask,
	state core.WorkflowInstanceState,
	executed, activities, timers []*history.Event,
	targets []*history.WorkflowEvent,
) error {
	encodedTargets := make([]*workflowTargetEvent, len(targets))
	for index, target := range targets {
		if target != nil {
			encodedTargets[index] = &workflowTargetEvent{
				Instance: target.WorkflowInstance,
				Event:    &workflowEvent{Event: target.HistoryEvent},
			}
		}
	}

	return b.write(ctx, workflowRequest{
		Operation: workflowCompleteTask, Task: encodeWorkflowTask(task),
		State: state, ExecutedEvents: encodeWorkflowEvents(executed),
		ActivityEvents: encodeWorkflowEvents(activities),
		TimerEvents:    encodeWorkflowEvents(timers),
		WorkflowEvents: encodedTargets,
	})
}

func (b *WorkflowBackend) GetActivityTask(
	ctx context.Context,
	queues []workflow.Queue,
) (*backend.ActivityTask, error) {
	task, err := workflowCall[*workflowActivity](ctx, b, workflowRequest{
		Operation: workflowGetActivity, Queues: queues,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get remote workflow activity")
	}

	return task.native(), nil
}

func (b *WorkflowBackend) ExtendActivityTask(
	ctx context.Context,
	task *backend.ActivityTask,
) error {
	return b.write(ctx, workflowRequest{
		Operation: workflowExtendActivity,
		Activity:  encodeWorkflowActivity(task),
	})
}

func (b *WorkflowBackend) CompleteActivityTask(
	ctx context.Context,
	task *backend.ActivityTask,
	result *history.Event,
) error {
	return b.write(ctx, workflowRequest{
		Operation: workflowCompleteActivity,
		Activity:  encodeWorkflowActivity(task),
		Event:     &workflowEvent{Event: result},
	})
}

func (b *WorkflowBackend) GetStats(_ context.Context) (*backend.Stats, error) {
	return nil, ctxerrors.Wrap(commerr.ErrPermissionDenied,
		"workflow statistics belong to the controller")
}

//nolint:ireturn // The workflow backend contract returns the tracer interface.
func (b *WorkflowBackend) Tracer() trace.Tracer {
	return b.options.TracerProvider.Tracer(backend.TracerName)
}

//nolint:ireturn // The workflow backend contract returns a metrics interface.
func (b *WorkflowBackend) Metrics() metrics.Client {
	return b.options.Metrics
}

func (b *WorkflowBackend) Options() *backend.Options {
	return b.options
}

func (b *WorkflowBackend) Close() error {
	return nil
}

func (b *WorkflowBackend) FeatureSupported(_ backend.Feature) bool {
	return false
}

func (b *WorkflowBackend) write(
	ctx context.Context,
	request workflowRequest,
) error {
	_, err := workflowCall[json.RawMessage](ctx, b, request)
	if err != nil {
		return ctxerrors.Wrap(err, "write remote workflow state")
	}

	return nil
}

//nolint:ireturn // The decoded value has the caller's concrete result type.
func workflowCall[R any](
	ctx context.Context,
	b *WorkflowBackend,
	request workflowRequest,
) (R, error) {
	var result R
	if b.conn == nil || b.sessionID == uuid.Nil {
		return result, ctxerrors.Wrap(
			commerr.ErrInvalidArgument, "workflow connection is required",
		)
	}

	encoded, err := b.conn.Call(ctx, KindCall, string(methodWorkflowBackend),
		InputRequest[workflowRequest]{SessionID: b.sessionID, Input: request},
	)
	if err != nil {
		return result, restoreWorkflowError(err)
	}

	if len(encoded) == 0 {
		return result, nil
	}

	if err := json.Unmarshal(encoded, &result); err != nil {
		return *new(R), ctxerrors.Wrap(err, "decode workflow backend answer")
	}

	return result, nil
}

func restoreWorkflowError(err error) error {
	switch {
	case errors.Is(err, commerr.ErrNotFound):
		return ctxerrors.Wrap(backend.ErrInstanceNotFound, "remote workflow")
	case errors.Is(err, commerr.ErrAlreadyExists):
		return ctxerrors.Wrap(
			backend.ErrInstanceAlreadyExists, "remote workflow",
		)
	default:
		return ctxerrors.Wrap(err, "remote workflow operation")
	}
}
