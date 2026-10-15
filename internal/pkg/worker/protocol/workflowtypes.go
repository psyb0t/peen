package protocol

import (
	"github.com/cschleiden/go-workflows/backend"
	"github.com/cschleiden/go-workflows/backend/history"
	"github.com/cschleiden/go-workflows/backend/metadata"
	"github.com/cschleiden/go-workflows/core"
	"github.com/cschleiden/go-workflows/workflow"
)

const methodWorkflowBackend Method = "workflow.backend"

type workflowOperation string

const (
	workflowCreateInstance   workflowOperation = "createInstance"
	workflowCancelInstance   workflowOperation = "cancelInstance"
	workflowGetState         workflowOperation = "getState"
	workflowGetHistory       workflowOperation = "getHistory"
	workflowSignal           workflowOperation = "signal"
	workflowPrepareQueues    workflowOperation = "prepareQueues"
	workflowGetTask          workflowOperation = "getWorkflowTask"
	workflowExtendTask       workflowOperation = "extendWorkflowTask"
	workflowCompleteTask     workflowOperation = "completeWorkflowTask"
	workflowGetActivity      workflowOperation = "getActivityTask"
	workflowExtendActivity   workflowOperation = "extendActivityTask"
	workflowCompleteActivity workflowOperation = "completeActivityTask"
)

type workflowRequest struct {
	Operation      workflowOperation          `json:"operation"`
	Instance       *workflow.Instance         `json:"instance,omitempty"`
	InstanceID     string                     `json:"instanceId,omitempty"`
	Event          *workflowEvent             `json:"event,omitempty"`
	Queues         []workflow.Queue           `json:"queues,omitempty"`
	LastSequenceID *int64                     `json:"lastSequenceId,omitempty"`
	State          core.WorkflowInstanceState `json:"state,omitempty"`
	Task           *workflowTask              `json:"task,omitempty"`
	Activity       *workflowActivity          `json:"activity,omitempty"`
	ExecutedEvents []*workflowEvent           `json:"executedEvents,omitempty"`
	ActivityEvents []*workflowEvent           `json:"activityEvents,omitempty"`
	TimerEvents    []*workflowEvent           `json:"timerEvents,omitempty"`
	WorkflowEvents []*workflowTargetEvent     `json:"workflowEvents,omitempty"`
}

type workflowTask struct {
	ID             string                     `json:"id"`
	Queue          workflow.Queue             `json:"queue"`
	Instance       *workflow.Instance         `json:"instance"`
	State          core.WorkflowInstanceState `json:"state"`
	Metadata       *metadata.WorkflowMetadata `json:"metadata"`
	LastSequenceID int64                      `json:"lastSequenceId"`
	NewEvents      []*workflowEvent           `json:"newEvents"`
}

type workflowActivity struct {
	ID         string             `json:"id"`
	ActivityID string             `json:"activityId"`
	Queue      workflow.Queue     `json:"queue"`
	Instance   *workflow.Instance `json:"instance"`
	Event      *workflowEvent     `json:"event"`
}

type workflowTargetEvent struct {
	Instance *workflow.Instance `json:"instance"`
	Event    *workflowEvent     `json:"event"`
}

func encodeWorkflowEvents(events []*history.Event) []*workflowEvent {
	encoded := make([]*workflowEvent, len(events))
	for index, event := range events {
		if event != nil {
			encoded[index] = &workflowEvent{Event: event}
		}
	}

	return encoded
}

func decodeWorkflowEvents(events []*workflowEvent) []*history.Event {
	decoded := make([]*history.Event, len(events))
	for index, event := range events {
		if event != nil {
			decoded[index] = event.Event
		}
	}

	return decoded
}

func encodeWorkflowTask(task *backend.WorkflowTask) *workflowTask {
	if task == nil {
		return nil
	}

	return &workflowTask{
		ID: task.ID, Queue: task.Queue, Instance: task.WorkflowInstance,
		State: task.WorkflowInstanceState, Metadata: task.Metadata,
		LastSequenceID: task.LastSequenceID,
		NewEvents:      encodeWorkflowEvents(task.NewEvents),
	}
}

func (t *workflowTask) native() *backend.WorkflowTask {
	if t == nil {
		return nil
	}

	return &backend.WorkflowTask{
		ID: t.ID, Queue: t.Queue, WorkflowInstance: t.Instance,
		WorkflowInstanceState: t.State, Metadata: t.Metadata,
		LastSequenceID: t.LastSequenceID,
		NewEvents:      decodeWorkflowEvents(t.NewEvents),
	}
}

func encodeWorkflowActivity(task *backend.ActivityTask) *workflowActivity {
	if task == nil {
		return nil
	}

	return &workflowActivity{
		ID: task.ID, ActivityID: task.ActivityID, Queue: task.Queue,
		Instance: task.WorkflowInstance,
		Event:    &workflowEvent{Event: task.Event},
	}
}

func (t *workflowActivity) native() *backend.ActivityTask {
	if t == nil {
		return nil
	}

	var event *history.Event
	if t.Event != nil {
		event = t.Event.Event
	}

	return &backend.ActivityTask{
		ID: t.ID, ActivityID: t.ActivityID, Queue: t.Queue,
		WorkflowInstance: t.Instance, Event: event,
	}
}
