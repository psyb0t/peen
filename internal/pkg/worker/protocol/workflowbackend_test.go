package protocol

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/cschleiden/go-workflows/backend"
	"github.com/cschleiden/go-workflows/client"
	"github.com/cschleiden/go-workflows/core"
	workflowworker "github.com/cschleiden/go-workflows/worker"
	"github.com/cschleiden/go-workflows/workflow"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type workflowRPCFixture struct {
	controller *Controller
	remote     *WorkflowBackend
	storage    *db.WorkflowStorage
	sessionID  uuid.UUID
}

func newWorkflowRPCFixture(t *testing.T) workflowRPCFixture {
	t.Helper()
	controller, sessionID := newControllerFixture(t)
	ctx := t.Context()
	_, err := controller.store.CreateWorkerGeneration(ctx, sessionID,
		session.CreateWorkerGenerationInput{
			ID: controller.generationID, Kind: "control", AgentSpace: "control",
			State: "ready", Workspace: "/workspace",
			SocketPath: "/example/workflow.sock", CredentialHash: "EXAMPLE-HASH",
		})
	require.NoError(t, err)
	storage, err := db.OpenWorkflowStorage(ctx, db.Config{Directory: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.Close()) })
	require.NoError(t, controller.BindWorkflows(ctx, storage))
	workerSide, controllerSide := net.Pipe()
	server := NewConn(controllerSide, controller)
	worker := NewConn(workerSide, nil)
	serving, cancel := context.WithCancel(ctx)
	finished := make(chan error, 2)
	for _, connection := range []*Conn{server, worker} {
		go func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					finished <- ctxerrors.New("workflow RPC fixture panicked")
				}
			}()
			finished <- connection.Serve(serving)
		}()
	}
	t.Cleanup(func() {
		cancel()
		server.Close()
		worker.Close()
		for range 2 {
			select {
			case err := <-finished:
				require.NoError(t, err)
			case <-time.After(testEventuallyTimeout):
				t.Fatal("workflow RPC fixture did not stop")
			}
		}
	})

	return workflowRPCFixture{
		controller: controller, remote: NewWorkflowBackend(worker, sessionID),
		storage: storage, sessionID: sessionID,
	}
}

func rpcWorkflowActivity(context.Context) (string, error) {
	return "saved workflow result", nil
}

func rpcWorkflow(ctx workflow.Context) (string, error) {
	result, err := workflow.ExecuteActivity[string](
		ctx, workflow.DefaultActivityOptions, rpcWorkflowActivity,
	).Get(ctx)
	if err != nil {
		return "", ctxerrors.Wrap(err, "run workflow RPC activity")
	}

	return result, nil
}

func TestWorkflowBackendRunsNativeWorkerOverRPC(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowRPCFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	options := workflowworker.DefaultOptions
	queue := workflow.Queue(fixture.sessionID.String())
	options.WorkflowQueues = []workflow.Queue{queue}
	options.ActivityQueues = []workflow.Queue{queue}
	executor := workflowworker.New(fixture.remote, &options)
	require.NoError(t, executor.RegisterWorkflow(rpcWorkflow))
	require.NoError(t, executor.RegisterActivity(rpcWorkflowActivity))
	require.NoError(t, executor.Start(ctx))
	stop := sync.OnceFunc(func() {
		cancel()
		require.NoError(t, executor.WaitForCompletion())
	})
	t.Cleanup(stop)
	api := client.New(fixture.remote)
	instance, err := api.CreateWorkflowInstance(ctx,
		client.WorkflowInstanceOptions{
			InstanceID: WorkflowInstanceID(fixture.sessionID, uuid.New()),
			Queue:      queue,
		}, rpcWorkflow,
	)
	require.NoError(t, err)
	result, err := client.GetWorkflowResult[string](ctx, api, instance,
		testEventuallyTimeout)
	require.NoError(t, err)
	assert.Equal(t, "saved workflow result", result)
	history, err := fixture.storage.GetWorkflowInstanceHistory(ctx, instance, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, history)
	stop()
	require.NoError(t, fixture.remote.Close())
	_, err = fixture.storage.GetStats(t.Context())
	require.NoError(t, err)
}

func TestWorkflowBackendPreservesRemoteSentinels(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowRPCFixture(t)
	ctx := t.Context()
	api := client.New(fixture.remote)
	options := client.WorkflowInstanceOptions{
		InstanceID: WorkflowInstanceID(fixture.sessionID, uuid.New()),
		Queue:      workflow.Queue(fixture.sessionID.String()),
	}
	instance, err := api.CreateWorkflowInstance(ctx, options, rpcWorkflow)
	require.NoError(t, err)
	_, err = api.CreateWorkflowInstance(ctx, options, rpcWorkflow)
	require.ErrorIs(t, err, backend.ErrInstanceAlreadyExists)
	missing := *instance
	missing.ExecutionID = uuid.NewString()
	_, err = fixture.remote.GetWorkflowInstanceState(ctx, &missing)
	require.ErrorIs(t, err, backend.ErrInstanceNotFound)
}

func TestWorkflowBackendRejectsCrossSessionAccess(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowRPCFixture(t)
	ctx := t.Context()
	other := uuid.New()
	_, err := fixture.remote.GetWorkflowTask(ctx,
		[]workflow.Queue{workflow.Queue(other.String())})
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	_, err = client.New(fixture.remote).CreateWorkflowInstance(ctx,
		client.WorkflowInstanceOptions{
			InstanceID: WorkflowInstanceID(other, uuid.New()),
			Queue:      workflow.Queue(fixture.sessionID.String()),
		}, rpcWorkflow)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	forged := NewWorkflowBackend(fixture.remote.conn, other)
	_, err = forged.GetWorkflowTask(ctx,
		[]workflow.Queue{workflow.Queue(other.String())})
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
}

func TestWorkflowBackendRejectsInactiveGeneration(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowRPCFixture(t)
	ctx := t.Context()
	require.NoError(t, fixture.controller.store.UpdateWorkerGenerationState(
		ctx, fixture.controller.generationID, "stopped", "",
	))
	_, err := fixture.remote.GetWorkflowTask(ctx,
		[]workflow.Queue{workflow.Queue(fixture.sessionID.String())})
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
}

func TestWorkflowBackendRestrictsQueues(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowRPCFixture(t)
	queue := workflow.Queue(fixture.sessionID.String())
	testCases := []struct {
		name    string
		queues  []workflow.Queue
		wantErr error
	}{
		{name: "own", queues: []workflow.Queue{queue}},
		{name: "SDK system", queues: []workflow.Queue{queue, core.QueueSystem}},
		{name: "empty", wantErr: commerr.ErrPermissionDenied},
		{
			name: "system only", queues: []workflow.Queue{core.QueueSystem},
			wantErr: commerr.ErrPermissionDenied,
		},
		{
			name: "default", queues: []workflow.Queue{workflow.QueueDefault},
			wantErr: commerr.ErrPermissionDenied,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := fixture.remote.PrepareWorkflowQueues(t.Context(), tc.queues)
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestWorkflowBackendPreservesRetainedHistory(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowRPCFixture(t)
	assert.False(t, fixture.remote.FeatureSupported(backend.Feature_Expiration))
	require.ErrorIs(t, fixture.remote.RemoveWorkflowInstances(t.Context()),
		commerr.ErrPermissionDenied)
	require.ErrorIs(t, fixture.remote.RemoveWorkflowInstance(t.Context(), nil),
		commerr.ErrPermissionDenied)
}

type rpcWorkflowTree struct {
	InstanceID string            `json:"instanceId"`
	Children   []rpcWorkflowTree `json:"children"`
}

func rpcTreeWorkflow(ctx workflow.Context, tree rpcWorkflowTree) (int, error) {
	children := make([]workflow.Future[int], len(tree.Children))
	for index, child := range tree.Children {
		options := workflow.DefaultSubWorkflowOptions
		options.InstanceID = child.InstanceID
		children[index] = workflow.CreateSubWorkflowInstance[int](
			ctx, options, rpcTreeWorkflow, child,
		)
	}
	_, err := workflow.ExecuteActivity[string](
		ctx, workflow.DefaultActivityOptions, rpcWorkflowActivity,
	).Get(ctx)
	if err != nil {
		return 0, ctxerrors.Wrap(err, "run tree activity")
	}
	count := 1
	for _, child := range children {
		result, err := child.Get(ctx)
		if err != nil {
			return 0, ctxerrors.Wrap(err, "wait for tree child")
		}
		count += result
	}

	return count, nil
}

func TestWorkflowBackendRunsParallelNestedTree(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowRPCFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	queue := workflow.Queue(fixture.sessionID.String())
	options := workflowworker.DefaultOptions
	options.WorkflowQueues = []workflow.Queue{queue}
	options.ActivityQueues = []workflow.Queue{queue}
	executor := workflowworker.New(fixture.remote, &options)
	require.NoError(t, executor.RegisterWorkflow(rpcTreeWorkflow))
	require.NoError(t, executor.RegisterActivity(rpcWorkflowActivity))
	require.NoError(t, executor.Start(ctx))
	t.Cleanup(func() {
		cancel()
		require.NoError(t, executor.WaitForCompletion())
	})
	tree := rpcWorkflowTree{
		InstanceID: WorkflowInstanceID(fixture.sessionID, uuid.New()),
	}
	for _, grandchildren := range []int{3, 1, 0} {
		child := rpcWorkflowTree{
			InstanceID: WorkflowInstanceID(fixture.sessionID, uuid.New()),
		}
		for range grandchildren {
			child.Children = append(child.Children, rpcWorkflowTree{
				InstanceID: WorkflowInstanceID(fixture.sessionID, uuid.New()),
			})
		}
		tree.Children = append(tree.Children, child)
	}
	api := client.New(fixture.remote)
	instance, err := api.CreateWorkflowInstance(ctx,
		client.WorkflowInstanceOptions{InstanceID: tree.InstanceID, Queue: queue},
		rpcTreeWorkflow, tree,
	)
	require.NoError(t, err)
	result, err := client.GetWorkflowResult[int](ctx, api, instance,
		testEventuallyTimeout)
	require.NoError(t, err)
	assert.Equal(t, 8, result)
	stored, err := fixture.controller.workflows.backend.GetWorkflowTree(
		ctx, instance,
	)
	require.NoError(t, err)
	require.Len(t, stored.Children, 3)
	assert.Equal(t, core.WorkflowInstanceStateFinished, stored.State)
	byID := make(map[string]int)
	for _, child := range stored.Children {
		byID[child.Instance.InstanceID] = len(child.Children)
		assert.Equal(t, core.WorkflowInstanceStateFinished, child.State)
		for _, grandchild := range child.Children {
			assert.Equal(t, core.WorkflowInstanceStateFinished, grandchild.State)
		}
	}
	for _, child := range tree.Children {
		assert.Equal(t, len(child.Children), byID[child.InstanceID])
	}
}
