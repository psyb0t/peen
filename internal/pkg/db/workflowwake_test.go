package db

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/go-workflows/backend"
	"github.com/psyb0t/go-workflows/backend/history"
	"github.com/psyb0t/go-workflows/client"
	"github.com/psyb0t/go-workflows/core"
	"github.com/psyb0t/go-workflows/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type workflowClaim struct {
	task *backend.WorkflowTask
	err  error
	took time.Duration
}

func TestWorkflowTaskClaimWakesOnAWriteFromAnotherBackend(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	storage, err := OpenWorkflowStorage(ctx, Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.Close()) })
	waiting, err := storage.WorkerBackend(ctx, uuid.New())
	require.NoError(t, err)
	queues := []workflow.Queue{workflow.QueueDefault}

	claims := make(chan workflowClaim, 1)
	go func() {
		started := time.Now()
		task, err := waiting.GetWorkflowTask(ctx, queues)
		claims <- workflowClaim{task: task, err: err, took: time.Since(started)}
	}()
	instance, err := client.New(storage).CreateWorkflowInstance(
		ctx, client.WorkflowInstanceOptions{InstanceID: uuid.NewString()},
		storageWorkflow,
	)
	require.NoError(t, err)

	claim := <-claims
	require.NoError(t, claim.err)
	require.NotNil(t, claim.task)
	assert.Equal(t, instance, claim.task.WorkflowInstance)
	assert.Less(t, claim.took, workflowTaskWait)
}

func TestWorkflowTaskClaimAnswersEmptyAfterItsWait(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	storage, err := OpenWorkflowStorage(ctx, Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.Close()) })
	queues := []workflow.Queue{workflow.QueueDefault}

	started := time.Now()
	task, err := storage.GetActivityTask(ctx, queues)
	require.NoError(t, err)
	assert.Nil(t, task)
	assert.GreaterOrEqual(t, time.Since(started), workflowTaskWait)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	workflowTask, err := storage.GetWorkflowTask(cancelled, queues)
	require.NoError(t, err)
	assert.Nil(t, workflowTask)
}

func TestWorkflowWakeTargetsQueuesAndRetiresSubscriptions(t *testing.T) {
	t.Parallel()
	wake := newWorkflowWake()
	first := workflow.Queue("first")
	second := workflow.Queue("second")
	signal, unsubscribe := wake.subscribe([]workflow.Queue{first, second})
	wake.notify("unrelated")
	select {
	case <-signal:
		t.Fatal("unrelated queue woke the subscription")
	default:
	}

	for _, queue := range []workflow.Queue{first, second} {
		wake.notify(queue)
		wake.notify(queue)
		assert.Len(t, signal, 1)
		<-signal
	}

	wake.notify()
	assert.Len(t, signal, 1)
	<-signal
	unsubscribe()
	wake.notify(first)
	assert.Empty(t, signal)
	assert.Empty(t, wake.pending)
}

func TestWorkflowCompletionWakesCustomAndUnknownTargets(t *testing.T) {
	t.Parallel()
	wake := newWorkflowWake()
	custom := workflow.Queue("custom")
	signal, unsubscribe := wake.subscribe([]workflow.Queue{custom})
	defer unsubscribe()
	b := &wakingBackend{wake: wake}
	task := &backend.WorkflowTask{Queue: workflow.QueueDefault}
	b.notifyWorkflowCompletion(task, []*history.Event{{
		Attributes: &history.ActivityScheduledAttributes{Queue: custom},
	}}, nil)
	assert.Len(t, signal, 1)
	<-signal
	b.notifyWorkflowCompletion(task, nil, []*history.WorkflowEvent{{}})
	assert.Len(t, signal, 1)
}

func TestActivityCompletionWakesItsDifferentWorkflowQueue(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), testEventuallyTimeout)
	defer cancel()
	storage, err := OpenWorkflowStorage(ctx, Config{Directory: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.Close()) })
	_, err = client.New(storage).CreateWorkflowInstance(ctx,
		client.WorkflowInstanceOptions{InstanceID: uuid.NewString()},
		storageWorkflow)
	require.NoError(t, err)
	queues := []workflow.Queue{workflow.QueueDefault}
	task, err := storage.GetWorkflowTask(ctx, queues)
	require.NoError(t, err)
	require.NotNil(t, task)
	custom := workflow.Queue("customActivity")
	scheduled := history.NewPendingEvent(time.Now().UTC(),
		history.EventType_ActivityScheduled,
		&history.ActivityScheduledAttributes{
			Name: "storageActivity", Queue: custom,
		}, history.ScheduleEventID(1))
	executed := append(slices.Clone(task.NewEvents), scheduled)
	require.NoError(t, storage.CompleteWorkflowTask(ctx, task,
		core.WorkflowInstanceStateActive, executed,
		[]*history.Event{scheduled}, nil, nil))
	activity, err := storage.GetActivityTask(ctx, []workflow.Queue{custom})
	require.NoError(t, err)
	require.NotNil(t, activity)
	signal, unsubscribe := storage.wake.subscribe(queues)
	defer unsubscribe()
	completed := history.NewPendingEvent(time.Now().UTC(),
		history.EventType_ActivityCompleted,
		&history.ActivityCompletedAttributes{}, history.ScheduleEventID(1))
	require.NoError(t, storage.CompleteActivityTask(ctx, activity, completed))
	assert.Len(t, signal, 1)
	task, err = storage.GetWorkflowTask(ctx, queues)
	require.NoError(t, err)
	require.NotNil(t, task)
	assert.True(t, slices.ContainsFunc(task.NewEvents,
		func(event *history.Event) bool {
			return event.Type == history.EventType_ActivityCompleted
		}))
}
