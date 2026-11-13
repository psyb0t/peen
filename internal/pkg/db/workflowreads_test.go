package db

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/go-workflows/client"
	"github.com/psyb0t/go-workflows/core"
	"github.com/psyb0t/go-workflows/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The writer remains held until the read returns. This bounds a deadlock,
// rather than imposing a throughput threshold on a loaded CI machine.
const workflowReadDeadline = 5 * time.Second

func TestWorkflowReadsProceedDuringUncommittedWrite(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	storage, err := OpenWorkflowStorage(ctx, Config{Directory: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.Close()) })
	instance, err := client.New(storage).CreateWorkflowInstance(
		ctx, client.WorkflowInstanceOptions{InstanceID: uuid.NewString()},
		storageWorkflow,
	)
	require.NoError(t, err)
	borrowed, err := storage.WorkerBackend(ctx, uuid.New())
	require.NoError(t, err)
	tx, err := storage.sqlDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() {
		// Commit below releases it on success; rollback releases it on failure.
		_ = tx.Rollback()
	}()
	_, err = tx.ExecContext(ctx,
		"UPDATE instances SET state = ? WHERE id = ? AND execution_id = ?",
		core.WorkflowInstanceStateFinished,
		instance.InstanceID, instance.ExecutionID,
	)
	require.NoError(t, err)
	readCtx, cancel := context.WithTimeout(ctx, workflowReadDeadline)
	defer cancel()
	state, err := borrowed.GetWorkflowInstanceState(readCtx, instance)
	require.NoError(t, err)
	assert.Equal(t, core.WorkflowInstanceStateActive, state)
	_, err = borrowed.GetWorkflowInstanceHistory(readCtx, instance, nil)
	require.NoError(t, err)
	ref, err := borrowed.GetWorkflowInstance(readCtx, instance)
	require.NoError(t, err)
	require.NotNil(t, ref)
	assert.Equal(t, state, ref.State)
	require.NoError(t, tx.Commit())
	state, err = storage.GetWorkflowInstanceState(ctx, instance)
	require.NoError(t, err)
	assert.Equal(t, core.WorkflowInstanceStateFinished, state)
	require.NoError(t, borrowed.Close())
	_, err = storage.GetWorkflowInstanceState(ctx, instance)
	require.NoError(t, err)

	cancelled, stop := context.WithCancel(ctx)
	stop()
	_, err = borrowed.GetWorkflowInstanceState(cancelled, instance)
	require.ErrorIs(t, err, context.Canceled)
}

func TestWorkflowConcurrentSessionsReadAndWrite(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), testEventuallyTimeout)
	defer cancel()
	storage, err := OpenWorkflowStorage(ctx, Config{Directory: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.Close()) })
	const sessions = 10

	var group sync.WaitGroup

	for range sessions {
		group.Go(func() {
			queue := workflow.Queue(uuid.NewString())
			instance, createErr := client.New(storage).CreateWorkflowInstance(
				ctx, client.WorkflowInstanceOptions{
					InstanceID: uuid.NewString(), Queue: queue,
				}, storageWorkflow,
			)
			if !assert.NoError(t, createErr) {
				return
			}

			for range sessions {
				state, readErr := storage.GetWorkflowInstanceState(ctx, instance)
				if !assert.NoError(t, readErr) {
					return
				}

				assert.Equal(t, core.WorkflowInstanceStateActive, state)
			}

			task, claimErr := storage.GetWorkflowTask(ctx, []workflow.Queue{queue})
			if !assert.NoError(t, claimErr) || !assert.NotNil(t, task) {
				return
			}

			assert.Equal(t, instance, task.WorkflowInstance)
			assert.NoError(t, storage.CompleteWorkflowTask(
				ctx, task, core.WorkflowInstanceStateFinished,
				task.NewEvents, nil, nil, nil,
			))
		})
	}

	group.Wait()
	assert.NoError(t, ctx.Err())
}
