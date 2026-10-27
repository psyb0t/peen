package protocol

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/go-workflows/backend"
	"github.com/psyb0t/go-workflows/client"
	"github.com/psyb0t/go-workflows/workflow"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowControllerRejectsReplacedReadyWorker(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowRPCFixture(t)
	queues := []workflow.Queue{workflow.Queue(fixture.sessionID.String())}
	require.NoError(t, fixture.remote.PrepareWorkflowQueues(t.Context(), queues))
	_, err := fixture.controller.store.CreateWorkerGeneration(
		t.Context(), fixture.sessionID, session.CreateWorkerGenerationInput{
			ID: uuid.New(), Kind: "control", AgentSpace: "control",
			State: "ready", Workspace: "/workspace",
			SocketPath:     "/example/replacement.sock",
			CredentialHash: "EXAMPLE-HASH",
		},
	)
	require.NoError(t, err)
	err = fixture.remote.PrepareWorkflowQueues(t.Context(), queues)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
}

func TestWorkflowControllerRejectsIncompleteRequests(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowRPCFixture(t)
	testCases := []struct {
		name      string
		operation workflowOperation
		wantErr   error
	}{
		{
			name: "create", operation: workflowCreateInstance,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "cancel", operation: workflowCancelInstance,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "state", operation: workflowGetState,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "history", operation: workflowGetHistory,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "extend workflow", operation: workflowExtendTask,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "complete workflow", operation: workflowCompleteTask,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "extend activity", operation: workflowExtendActivity,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "complete activity", operation: workflowCompleteActivity,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "unknown", operation: workflowOperation("unknown"),
			wantErr: commerr.ErrNotImplemented,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := workflowCall[json.RawMessage](t.Context(), fixture.remote,
				workflowRequest{Operation: tc.operation})
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestWorkflowControllerReleasedClaimsLeaveNewTasksUnlocked(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowRPCFixture(t)
	ctx := t.Context()
	queue := workflow.Queue(fixture.sessionID.String())
	queues := []workflow.Queue{queue}

	type claim struct {
		task *backend.WorkflowTask
		err  error
	}
	waiting := make(chan claim, 1)
	go func() {
		task, err := fixture.remote.GetWorkflowTask(ctx, queues)
		waiting <- claim{task: task, err: err}
	}()
	require.NoError(t, fixture.remote.ReleaseClaims(ctx))
	instance, err := client.New(fixture.storage).CreateWorkflowInstance(ctx,
		client.WorkflowInstanceOptions{
			InstanceID: WorkflowInstanceID(fixture.sessionID, uuid.New()),
			Queue:      queue,
		}, rpcWorkflow,
	)
	require.NoError(t, err)

	released := <-waiting
	require.NoError(t, released.err)
	assert.Nil(t, released.task)
	late, err := fixture.remote.GetActivityTask(ctx, queues)
	require.NoError(t, err)
	assert.Nil(t, late)
	claimed, err := fixture.storage.GetWorkflowTask(ctx, queues)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, instance, claimed.WorkflowInstance)
}
