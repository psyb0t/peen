package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/go-workflows/backend"
	"github.com/psyb0t/go-workflows/backend/history"
	"github.com/psyb0t/go-workflows/core"
	"github.com/psyb0t/go-workflows/diag"
	"github.com/psyb0t/go-workflows/workflow"
	"github.com/psyb0t/peen/internal/pkg/db"
)

const (
	workflowInstanceSeparator  = "/"
	maximumWorkflowParentDepth = 128
)

type workflowController struct {
	backend   diag.Backend
	sessionID uuid.UUID
	// historyPageBytes bounds one history page. Tests lower it to cover
	// paging without histories near the frame limit.
	historyPageBytes int
	// released ends every waiting task claim once the worker stops polling.
	// A claim that woke after that would lock a task to a poller that is
	// gone, and the next generation would wait for the lease to expire.
	released     context.Context //nolint:containedctx // Release signal.
	releaseClaim context.CancelFunc
}

// WorkflowInstanceID scopes an execution's stable identity to its session.
func WorkflowInstanceID(sessionID, executionID uuid.UUID) string {
	return fmt.Sprintf("%s/%s", sessionID, executionID)
}

// BindWorkflows attaches controller-owned storage to this authenticated
// generation. A borrowed backend never opens another database connection.
func (c *Controller) BindWorkflows(
	ctx context.Context,
	storage *db.WorkflowStorage,
) error {
	if storage == nil || c.sessionID == uuid.Nil || c.generationID == uuid.Nil {
		return ctxerrors.Wrap(
			commerr.ErrInvalidArgument,
			"workflow connection identity is required",
		)
	}

	bound, err := storage.WorkerBackend(ctx, c.generationID)
	if err != nil {
		return ctxerrors.Wrap(err, "bind workflow worker backend")
	}

	released, release := context.WithCancel(context.WithoutCancel(ctx))
	c.workflows = &workflowController{
		backend: bound, sessionID: c.sessionID,
		historyPageBytes: workflowHistoryPageBytes,
		released:         released, releaseClaim: release,
	}

	return nil
}

func (c *Controller) handleWorkflowCall(
	ctx context.Context,
	payload json.RawMessage,
) (any, error) {
	if c.workflows == nil {
		return nil, ctxerrors.Wrap(
			commerr.ErrNotImplemented, "workflow storage is not attached",
		)
	}

	request, err := decode[InputRequest[workflowRequest]](payload)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "decode workflow backend request")
	}

	if err := c.assertSession(request.SessionID); err != nil {
		return nil, ctxerrors.Wrap(err, "authorize workflow session")
	}

	if err := c.requireCurrentWorkflowWorker(ctx); err != nil {
		return nil, ctxerrors.Wrap(err, "authorize workflow generation")
	}

	result, err := c.workflows.call(ctx, request.Input)
	if err != nil {
		return nil, workflowTransportError(err)
	}

	return result, nil
}

func workflowTransportError(err error) error {
	switch {
	case errors.Is(err, backend.ErrInstanceNotFound):
		return ctxerrors.Wrap(
			commerr.ErrNotFound, "workflow instance not found",
		)
	case errors.Is(err, backend.ErrInstanceAlreadyExists):
		return ctxerrors.Wrap(
			commerr.ErrAlreadyExists, "workflow instance already exists",
		)
	default:
		return ctxerrors.Wrap(err, "workflow backend operation")
	}
}

//nolint:cyclop // Dispatch includes each operation's required input guards.
func (c *workflowController) call(
	ctx context.Context,
	r workflowRequest,
) (any, error) {
	//nolint:exhaustive // Task operations are dispatched by callTask.
	switch r.Operation {
	case workflowCreateInstance:
		return c.create(ctx, r)
	case workflowCancelInstance:
		return c.cancel(ctx, r)
	case workflowGetState:
		if err := c.ownedInstance(ctx, r.Instance); err != nil {
			return nil, err
		}

		state, err := c.backend.GetWorkflowInstanceState(ctx, r.Instance)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "read workflow state")
		}

		return state, nil
	case workflowGetHistory:
		return c.getHistory(ctx, r)
	case workflowSignal:
		if err := c.instanceID(r.InstanceID); err != nil {
			return nil, err
		}

		if r.Event == nil || r.Event.Event == nil ||
			r.Event.Type != history.EventType_SignalReceived {
			return nil, invalidWorkflowRequest()
		}

		err := c.backend.SignalWorkflow(ctx, r.InstanceID, r.Event.Event)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "signal workflow instance")
		}

		return struct{}{}, nil
	case workflowPrepareQueues:
		return nil, c.queues(r.Queues)
	case workflowReleaseClaims:
		c.releaseClaim()

		return struct{}{}, nil
	default:
		return c.callTask(ctx, r)
	}
}

//nolint:cyclop // Each task operation validates its session-owned input.
func (c *workflowController) callTask(
	ctx context.Context,
	r workflowRequest,
) (any, error) {
	//nolint:exhaustive // Instance operations are dispatched by call.
	switch r.Operation {
	case workflowGetTask:
		if err := c.queues(r.Queues); err != nil {
			return nil, err
		}

		claimCtx, cancel := c.claimContext(ctx)
		defer cancel()

		if claimCtx.Err() != nil {
			return encodeWorkflowTask(nil), nil
		}

		task, err := c.backend.GetWorkflowTask(claimCtx,
			[]workflow.Queue{c.queue()})
		if err != nil {
			return nil, ctxerrors.Wrap(err, "claim workflow task")
		}

		return encodeWorkflowTask(task), nil
	case workflowExtendTask:
		if err := c.workflowTask(ctx, r.Task); err != nil {
			return nil, err
		}

		err := c.backend.ExtendWorkflowTask(ctx, r.Task.native())
		if err != nil {
			return nil, ctxerrors.Wrap(err, "extend workflow task")
		}

		return struct{}{}, nil
	case workflowCompleteTask:
		return nil, c.completeWorkflow(ctx, r)
	case workflowGetActivity:
		return c.getActivity(ctx, r.Queues)
	case workflowExtendActivity:
		if err := c.activityTask(ctx, r.Activity); err != nil {
			return nil, err
		}

		err := c.backend.ExtendActivityTask(ctx, r.Activity.native())
		if err != nil {
			return nil, ctxerrors.Wrap(err, "extend workflow activity")
		}

		return struct{}{}, nil
	case workflowCompleteActivity:
		return nil, c.completeActivity(ctx, r)
	}

	return nil, ctxerrors.Wrap(
		commerr.ErrNotImplemented, "workflow operation is not published",
	)
}

func (c *workflowController) cancel(
	ctx context.Context,
	r workflowRequest,
) (any, error) {
	if err := c.ownedInstance(ctx, r.Instance); err != nil {
		return nil, err
	}

	if r.Event == nil || r.Event.Event == nil ||
		r.Event.Type != history.EventType_WorkflowExecutionCanceled {
		return nil, invalidWorkflowRequest()
	}

	err := c.backend.CancelWorkflowInstance(ctx, r.Instance, r.Event.Event)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "cancel workflow instance")
	}

	return struct{}{}, nil
}

// claimContext ends a task claim with the connection or with the worker's
// release, whichever comes first.
func (c *workflowController) claimContext(
	ctx context.Context,
) (context.Context, context.CancelFunc) {
	claimCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.released, cancel)

	return claimCtx, func() {
		stop()
		cancel()
	}
}

func (c *workflowController) queue() workflow.Queue {
	return workflow.Queue(c.sessionID.String())
}

func (c *workflowController) getHistory(
	ctx context.Context,
	r workflowRequest,
) (any, error) {
	if err := c.ownedInstance(ctx, r.Instance); err != nil {
		return nil, err
	}

	if r.LastSequenceID != nil && *r.LastSequenceID < 0 {
		return nil, invalidWorkflowRequest()
	}

	events, err := c.backend.GetWorkflowInstanceHistory(
		ctx, r.Instance, r.LastSequenceID,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read workflow history")
	}

	return workflowHistoryPageOf(encodeWorkflowEvents(events),
		c.historyPageBytes)
}

// workflowHistoryPageOf returns the events that fit one page of budget bytes,
// always at least one, so every read makes progress.
func workflowHistoryPageOf(
	events []*workflowEvent,
	budget int,
) (workflowHistoryPage, error) {
	page := workflowHistoryPage{Events: make([]*workflowEvent, 0, len(events))}
	size := 0

	for index, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return workflowHistoryPage{}, ctxerrors.Wrap(err,
				"measure workflow history event")
		}

		if index > 0 && size+len(encoded) > budget {
			page.More = true

			break
		}

		size += len(encoded)

		page.Events = append(page.Events, event)
	}

	return page, nil
}

func (c *workflowController) getActivity(
	ctx context.Context,
	queues []workflow.Queue,
) (any, error) {
	if err := c.queues(queues); err != nil {
		return nil, err
	}

	claimCtx, cancel := c.claimContext(ctx)
	defer cancel()

	if claimCtx.Err() != nil {
		return encodeWorkflowActivity(nil), nil
	}

	task, err := c.backend.GetActivityTask(claimCtx,
		[]workflow.Queue{c.queue()})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "claim workflow activity")
	}

	if task != nil {
		task.Queue = c.queue()
	}

	return encodeWorkflowActivity(task), nil
}

func (c *workflowController) queues(queues []workflow.Queue) error {
	// The SDK adds its global system queue. Session workers never poll it.
	hasSession := false

	for _, queue := range queues {
		if queue == c.queue() {
			hasSession = true

			continue
		}

		if queue == core.QueueSystem {
			continue
		}

		return ctxerrors.Wrap(
			commerr.ErrPermissionDenied,
			"workflow queue belongs to another session",
		)
	}

	if !hasSession {
		return ctxerrors.Wrap(
			commerr.ErrPermissionDenied,
			"workflow queue belongs to another session",
		)
	}

	return nil
}

func (c *workflowController) instanceID(value string) error {
	session, execution, found := strings.Cut(value, workflowInstanceSeparator)
	if !found || session != c.sessionID.String() {
		return ctxerrors.Wrap(
			commerr.ErrPermissionDenied, "workflow belongs to another session",
		)
	}

	id, err := uuid.Parse(execution)
	if err != nil || id == uuid.Nil {
		return invalidWorkflowRequest()
	}

	return nil
}

func (c *workflowController) instance(instance *workflow.Instance) error {
	for depth := 0; instance != nil; depth++ {
		if depth >= maximumWorkflowParentDepth {
			return invalidWorkflowRequest()
		}

		if err := c.instanceID(instance.InstanceID); err != nil {
			return err
		}

		id, err := uuid.Parse(instance.ExecutionID)
		if err != nil || id == uuid.Nil || instance.ParentEventID < 0 {
			return invalidWorkflowRequest()
		}

		instance = instance.Parent
	}

	return nil
}

func (c *workflowController) ownedInstance(
	ctx context.Context,
	instance *workflow.Instance,
) error {
	if instance == nil {
		return invalidWorkflowRequest()
	}

	if err := c.instance(instance); err != nil {
		return err
	}

	stored, err := c.backend.GetWorkflowInstance(ctx, instance)
	if err != nil {
		return ctxerrors.Wrap(err, "find workflow instance owner")
	}

	if stored == nil {
		return ctxerrors.Wrap(backend.ErrInstanceNotFound, "workflow owner")
	}

	if stored.Queue != string(c.queue()) {
		return ctxerrors.Wrap(
			commerr.ErrPermissionDenied, "workflow queue owner",
		)
	}

	return nil
}

func invalidWorkflowRequest() error {
	return ctxerrors.Wrap(commerr.ErrValidationFailed, "workflow request shape")
}

//nolint:cyclop // Creation validates the instance, event, queue, and parent.
func (c *workflowController) create(
	ctx context.Context,
	r workflowRequest,
) (any, error) {
	if r.Instance == nil || r.Event == nil || r.Event.Event == nil {
		return nil, invalidWorkflowRequest()
	}

	if err := c.instance(r.Instance); err != nil {
		return nil, err
	}

	attributes, ok := r.Event.Attributes.(*history.ExecutionStartedAttributes)
	if !ok || attributes == nil || attributes.Name == "" ||
		r.Event.Type != history.EventType_WorkflowExecutionStarted {
		return nil, invalidWorkflowRequest()
	}

	if err := c.queues([]workflow.Queue{attributes.Queue}); err != nil {
		return nil, err
	}

	if r.Instance.Parent != nil {
		if err := c.ownedInstance(ctx, r.Instance.Parent); err != nil {
			return nil, err
		}
	}

	err := c.backend.CreateWorkflowInstance(ctx, r.Instance, r.Event.Event)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create workflow instance")
	}

	return struct{}{}, nil
}

func (c *workflowController) workflowTask(
	ctx context.Context,
	task *workflowTask,
) error {
	if task == nil || task.ID == "" || task.LastSequenceID < 0 {
		return invalidWorkflowRequest()
	}

	if err := c.queues([]workflow.Queue{task.Queue}); err != nil {
		return err
	}

	return c.ownedInstance(ctx, task.Instance)
}

func (c *workflowController) activityTask(
	ctx context.Context,
	task *workflowActivity,
) error {
	if task == nil || task.ID == "" || task.ActivityID != task.ID ||
		task.Event == nil || task.Event.Event == nil ||
		task.Event.ID != task.ID ||
		task.Event.Type != history.EventType_ActivityScheduled {
		return invalidWorkflowRequest()
	}

	if err := c.queues([]workflow.Queue{task.Queue}); err != nil {
		return err
	}

	return c.ownedInstance(ctx, task.Instance)
}

func (c *workflowController) completeActivity(
	ctx context.Context,
	r workflowRequest,
) error {
	if err := c.activityTask(ctx, r.Activity); err != nil {
		return err
	}

	if r.Event == nil || r.Event.Event == nil ||
		r.Event.ScheduleEventID != r.Activity.Event.ScheduleEventID {
		return invalidWorkflowRequest()
	}

	if r.Event.Type != history.EventType_ActivityCompleted &&
		r.Event.Type != history.EventType_ActivityFailed {
		return invalidWorkflowRequest()
	}

	err := c.backend.CompleteActivityTask(
		ctx, r.Activity.native(), r.Event.Event,
	)
	if err != nil {
		return ctxerrors.Wrap(err, "complete workflow activity")
	}

	return nil
}

func (c *workflowController) completeWorkflow(
	ctx context.Context,
	r workflowRequest,
) error {
	if err := c.workflowTask(ctx, r.Task); err != nil {
		return err
	}

	switch r.State {
	case core.WorkflowInstanceStateActive,
		core.WorkflowInstanceStateContinuedAsNew,
		core.WorkflowInstanceStateFinished:
	default:
		return invalidWorkflowRequest()
	}

	if err := c.validateWorkflowEvents(ctx, r); err != nil {
		return err
	}

	targets := make([]*history.WorkflowEvent, len(r.WorkflowEvents))
	for index, target := range r.WorkflowEvents {
		targets[index] = &history.WorkflowEvent{
			WorkflowInstance: target.Instance, HistoryEvent: target.Event.Event,
		}
	}

	err := c.backend.CompleteWorkflowTask(
		ctx, r.Task.native(), r.State,
		decodeWorkflowEvents(r.ExecutedEvents),
		decodeWorkflowEvents(r.ActivityEvents),
		decodeWorkflowEvents(r.TimerEvents), targets,
	)
	if err != nil {
		return ctxerrors.Wrap(err, "checkpoint workflow task")
	}

	return nil
}

func (c *workflowController) validateWorkflowEvents(
	ctx context.Context,
	r workflowRequest,
) error {
	for _, group := range [][]*workflowEvent{
		r.ExecutedEvents, r.ActivityEvents, r.TimerEvents,
	} {
		for _, event := range group {
			if event == nil || event.Event == nil {
				return invalidWorkflowRequest()
			}
		}
	}

	return c.validateActivityEvents(ctx, r)
}

func (c *workflowController) validateActivityEvents(
	ctx context.Context,
	r workflowRequest,
) error {
	for _, event := range r.ActivityEvents {
		value := event.Attributes

		attributes, ok := value.(*history.ActivityScheduledAttributes)
		if !ok || attributes == nil || attributes.Name == "" {
			return invalidWorkflowRequest()
		}

		if attributes.Queue != "" && attributes.Queue != c.queue() {
			return ctxerrors.Wrap(
				commerr.ErrPermissionDenied, "activity queue owner",
			)
		}
	}

	return c.validateWorkflowTargets(ctx, r.WorkflowEvents)
}

func (c *workflowController) validateWorkflowTargets(
	ctx context.Context,
	targets []*workflowTargetEvent,
) error {
	for _, target := range targets {
		if err := c.validateWorkflowTarget(ctx, target); err != nil {
			return err
		}
	}

	return nil
}

func (c *workflowController) validateWorkflowTarget(
	ctx context.Context,
	target *workflowTargetEvent,
) error {
	if target == nil || target.Instance == nil || target.Event == nil ||
		target.Event.Event == nil {
		return invalidWorkflowRequest()
	}

	if err := c.instance(target.Instance); err != nil {
		return err
	}

	if target.Event.Type != history.EventType_WorkflowExecutionStarted {
		if err := c.ownedInstance(ctx, target.Instance); err != nil {
			return err
		}

		return nil
	}

	return c.validateWorkflowStart(target.Event)
}

func (c *workflowController) validateWorkflowStart(event *workflowEvent) error {
	attributes, ok := event.Attributes.(*history.ExecutionStartedAttributes)
	if !ok || attributes == nil || attributes.Name == "" {
		return invalidWorkflowRequest()
	}

	if attributes.Queue != "" && attributes.Queue != c.queue() {
		return ctxerrors.Wrap(
			commerr.ErrPermissionDenied, "child queue owner",
		)
	}

	return nil
}
