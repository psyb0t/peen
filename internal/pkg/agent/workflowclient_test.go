package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cschleiden/go-workflows/backend"
	"github.com/cschleiden/go-workflows/backend/history"
	"github.com/cschleiden/go-workflows/client"
	"github.com/cschleiden/go-workflows/core"
	"github.com/cschleiden/go-workflows/workflow"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestWorkflowClientReusesCompletedInstanceWithoutAnotherModelCall(
	t *testing.T,
) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("one completed result"))
	fixture := newExecutionLoopFixture(t, driver, false)
	api := newWorkflowClient(fixture.storage)
	options := client.WorkflowInstanceOptions{
		InstanceID: fmt.Sprintf("%s/%s", fixture.input.SessionID,
			fixture.input.ExecutionID),
		Queue: workflow.Queue(fixture.input.SessionID.String()),
	}
	first, err := api.CreateWorkflowInstance(t.Context(), options,
		agentWorkflowName, fixture.input)
	require.NoError(t, err)
	require.NoError(t, fixture.worker.Start(fixture.engine.options.Lifecycle))
	fixture.started.Store(true)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		api, first, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "one completed result", result.Text)
	before, err := fixture.storage.GetWorkflowInstanceHistory(t.Context(),
		first, nil)
	require.NoError(t, err)
	repeated, err := api.CreateWorkflowInstance(t.Context(), options,
		agentWorkflowName, fixture.input)
	require.NoError(t, err)
	assert.Equal(t, first, repeated)
	again, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		api, repeated, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, result, again)
	after, err := fixture.storage.GetWorkflowInstanceHistory(t.Context(),
		first, nil)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.Len(t, driver.Requests(), 1)
	assertWorkflowClientReopened(t, fixture, first, options)
}

func assertWorkflowClientReopened(
	t *testing.T,
	fixture executionLoopFixture,
	original *workflow.Instance,
	options client.WorkflowInstanceOptions,
) {
	t.Helper()
	fixture.stop()
	require.NoError(t, fixture.storage.Close())
	storage, err := db.OpenWorkflowStorage(t.Context(),
		db.Config{Directory: fixture.base.stateDirectory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.Close()) })
	api := newWorkflowClient(storage)
	restored, err := api.CreateWorkflowInstance(t.Context(), options,
		agentWorkflowName, fixture.input)
	require.NoError(t, err)
	assert.Equal(t, original, restored)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		api, restored, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "one completed result", result.Text)
}

func TestWorkflowClientReconcilesLostAcknowledgmentAndPropagatesErrors(
	t *testing.T,
) {
	t.Parallel()
	testCases := []struct {
		name         string
		readErr      error
		createErr    error
		reconcileErr error
		wantErr      error
	}{
		{
			name: "lookup failure", readErr: commerr.ErrPermissionDenied,
			wantErr: commerr.ErrPermissionDenied,
		},
		{
			name: "lost acknowledgment", readErr: backend.ErrInstanceNotFound,
			createErr: commerr.ErrFetchFailed,
		},
		{
			name: "failed creation", readErr: backend.ErrInstanceNotFound,
			createErr:    commerr.ErrFetchFailed,
			reconcileErr: backend.ErrInstanceNotFound,
			wantErr:      commerr.ErrFetchFailed,
		},
		{
			name: "conflicting instance", readErr: backend.ErrInstanceNotFound,
			createErr:    backend.ErrInstanceAlreadyExists,
			reconcileErr: backend.ErrInstanceNotFound,
			wantErr:      backend.ErrInstanceAlreadyExists,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			storage := &backend.MockBackend{}
			t.Cleanup(func() { storage.AssertExpectations(t) })
			storage.On("GetWorkflowInstanceState", mock.Anything, mock.Anything).
				Return(core.WorkflowInstanceStateActive, tc.readErr).Once()
			if errors.Is(tc.readErr, backend.ErrInstanceNotFound) {
				storage.On("CreateWorkflowInstance", mock.Anything,
					mock.Anything, mock.Anything).Return(tc.createErr).Once()
				storage.On("GetWorkflowInstanceState", mock.Anything,
					mock.Anything).Return(core.WorkflowInstanceStateActive,
					tc.reconcileErr).Once()
			}

			adapter := &workflowSubmissionBackend{Backend: storage}
			err := adapter.CreateWorkflowInstance(t.Context(),
				&workflow.Instance{InstanceID: "saved-instance"}, &history.Event{})
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestWorkflowClientRefusesCancelledSubmission(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	adapter := &workflowSubmissionBackend{}
	err := adapter.CreateWorkflowInstance(ctx,
		&workflow.Instance{InstanceID: "saved-instance"}, &history.Event{})
	require.ErrorIs(t, err, context.Canceled)
}
