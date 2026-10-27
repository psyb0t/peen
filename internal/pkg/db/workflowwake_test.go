package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/go-workflows/backend"
	"github.com/psyb0t/go-workflows/client"
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
