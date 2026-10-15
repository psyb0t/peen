package agent

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/cschleiden/go-workflows/backend"
	"github.com/cschleiden/go-workflows/backend/history"
	"github.com/cschleiden/go-workflows/client"
	"github.com/cschleiden/go-workflows/workflow"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rootSubmissionReadFailureStore struct {
	*session.Store
}

func (s rootSubmissionReadFailureStore) GetExecution(
	context.Context,
	uuid.UUID,
	uuid.UUID,
) (*models.Execution, error) {
	return nil, ctxerrors.Wrap(commerr.ErrFetchFailed,
		"inject root workflow version read failure")
}

type rootSubmissionTimeoutBackend struct {
	backend.Backend
}

func (b rootSubmissionTimeoutBackend) CreateWorkflowInstance(
	context.Context,
	*workflow.Instance,
	*history.Event,
) error {
	return ctxerrors.Wrap(context.DeadlineExceeded,
		"inject root workflow scheduling timeout")
}

func newRootSubmissionFixture(t *testing.T) (
	executionLoopFixture, *preparedTurn, *workflowDispatch,
) {
	t.Helper()
	base := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("submittedRead", toolNameReadFile,
			`{"path":"submitted.txt"}`), elelemtest.Text("submitted result")))
	writeRuntimeFile(t, filepath.Join(base.workspace, "submitted.txt"),
		"submitted source")
	prepared, err := base.runtime.prepareTurn(t.Context(), TurnRequest{
		RequestID: uuid.New(), Workspace: base.workspace,
		Message: "inspect saved workspace",
	})
	require.NoError(t, err)
	prepared.attachPublisher(t.Context())
	require.NoError(t, base.runtime.registerActiveUserMessageQueue(prepared))
	t.Cleanup(func() { base.runtime.closeActiveUserMessageQueue(prepared) })
	require.NoError(t, base.runtime.startLease(t.Context(), prepared))
	fixture := newExecutionLoopFixtureWithInput(t, base,
		workflowExecutionInput{SessionID: prepared.lease.SessionID}, true)
	dispatch := newWorkflowDispatch(fixture.engine.options.Lifecycle,
		base.runtime, fixture.engine.options.GenerationID, nil)
	dispatch.bind(&fixture.engine.options)
	require.NoError(t, fixture.worker.Start(fixture.engine.options.Lifecycle))
	fixture.started.Store(true)

	return fixture, prepared, dispatch
}

func TestRootSubmissionKeepsOriginalLeaseAndEvents(t *testing.T) {
	t.Parallel()
	fixture, prepared, dispatch := newRootSubmissionFixture(t)
	original := prepared.lease
	var cancelled atomic.Bool
	fixture.base.store.RegisterCancellation(original,
		func() { cancelled.Store(true) })
	var delivered atomic.Int64
	prepared.turn.sink = func(Event) error {
		delivered.Add(1)

		return nil
	}
	submission, err := dispatch.submitRoot(t.Context(), fixture.api, prepared)
	require.NoError(t, err)
	assert.Equal(t, original, prepared.lease)
	assert.False(t, cancelled.Load())
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, submission.Instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, "submitted result", result.Text)
	assert.Positive(t, delivered.Load())
	turn, err := fixture.base.store.GetExecutionRootTurn(t.Context(),
		submission.Input.SessionID, submission.Input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, original.TurnID, turn.ID)
	assert.Equal(t, models.TurnStateCompleted, turn.State)
	assert.False(t, cancelled.Load())
	prepared.turn.mutex.Lock()
	defer prepared.turn.mutex.Unlock()
	assert.Equal(t, EventTypeUserMessageCreated, prepared.turn.events[0].Type)
	assert.Equal(t, EventTypeTurnCompleted,
		prepared.turn.events[len(prepared.turn.events)-1].Type)
}

func TestRootSubmissionUsesPhasedWorkflow(t *testing.T) {
	t.Parallel()
	fixture, prepared, dispatch := newRootSubmissionFixture(t)
	submission, err := dispatch.submitRoot(t.Context(), fixture.api, prepared)
	require.NoError(t, err)
	_, err = client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, submission.Instance, workflowTestResultTimeout)
	require.NoError(t, err)
	execution, err := fixture.base.store.GetExecution(t.Context(),
		submission.Input.SessionID, submission.Input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, agentWorkflowName, execution.WorkflowVersion)
	page, err := fixture.base.store.ListExecutionActivities(t.Context(),
		execution.SessionID, execution.ID, session.ListEventsOptions{})
	require.NoError(t, err)
	steps := []string{}
	for _, activity := range page.Items {
		if activity.Name != toolPhaseActivityName {
			continue
		}
		assert.Equal(t, toolPhaseActivityName, activity.Name)
		assert.Equal(t, models.ActivityStateCompleted, activity.State)
		steps = append(steps, activity.StepID)
	}
	assert.ElementsMatch(t, []string{
		"tool:0:0:preHook", "tool:0:0:handler", "tool:0:0:outcomeHook",
		"tool:0:0:postHook", "tool:0:0:project",
	}, steps)
}

func TestRootSubmissionSchedulesToolPhases(t *testing.T) {
	t.Parallel()
	fixture, prepared, dispatch := newRootSubmissionFixture(t)
	input, err := dispatch.prepareRootSubmission(t.Context(), prepared)
	require.NoError(t, err)
	submission, err := dispatch.submitRootWorkflow(t.Context(), fixture.api,
		input)
	require.NoError(t, err)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, submission.Instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, "submitted result", result.Text)
	page, err := fixture.base.store.ListExecutionActivities(t.Context(),
		input.SessionID, input.ExecutionID, session.ListEventsOptions{})
	require.NoError(t, err)
	names := []string{}
	for _, activity := range page.Items {
		if activity.Name == toolPhaseActivityName {
			names = append(names, activity.Name)
		}
	}
	assert.ElementsMatch(t, []string{toolPhaseActivityName, toolPhaseActivityName, toolPhaseActivityName, toolPhaseActivityName, toolPhaseActivityName}, names)
}

func TestRootSubmissionRefusesForeignSession(t *testing.T) {
	t.Parallel()
	fixture, prepared, dispatch := newRootSubmissionFixture(t)
	prepared.lease.SessionID = uuid.New()
	_, err := dispatch.submitRoot(t.Context(), fixture.api, prepared)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	page, err := fixture.base.store.SearchExecutions(t.Context(),
		fixture.base.runtime.sessionID, session.SearchExecutionsOptions{})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}

func TestRootWorkflowSubmissionRefusesUnavailableRecord(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name                                               string
		missingStorage, foreign, missingRecord, failedRead bool
		wantErr                                            error
	}{
		{
			name: "missing storage", missingStorage: true,
			wantErr: commerr.ErrRequiredFieldNotSet,
		},
		{
			name: "foreign session", foreign: true,
			wantErr: commerr.ErrPermissionDenied,
		},
		{
			name: "missing record", missingRecord: true,
			wantErr: commerr.ErrNotFound,
		},
		{
			name: "record read failure", failedRead: true,
			wantErr: commerr.ErrFetchFailed,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, prepared, dispatch := newRootSubmissionFixture(t)
			input, err := dispatch.prepareRootSubmission(t.Context(), prepared)
			require.NoError(t, err)
			if tc.missingStorage {
				dispatch.runtime.store = struct{ session.Storage }{fixture.base.store}
			}
			if tc.foreign {
				input.SessionID = uuid.New()
			}
			if tc.missingRecord {
				input.ExecutionID = uuid.New()
			}
			if tc.failedRead {
				dispatch.runtime.store = rootSubmissionReadFailureStore{
					Store: fixture.base.store,
				}
			}
			submission, err := dispatch.submitRootWorkflow(t.Context(), fixture.api,
				input)
			require.ErrorIs(t, err, tc.wantErr)
			assert.Equal(t, input, submission.Input)
			assert.Nil(t, submission.Instance)
			stats, err := fixture.storage.GetStats(t.Context())
			require.NoError(t, err)
			assert.Zero(t, stats.ActiveWorkflowInstances)
		})
	}
}

func TestRootWorkflowSubmissionRefusesChildRecord(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childModelFixture(t)
	dispatch := newWorkflowDispatch(t.Context(), fixture.base.runtime,
		fixture.engine.options.GenerationID, nil)
	submission, err := dispatch.submitRootWorkflow(t.Context(), fixture.api,
		input.Execution)
	require.ErrorIs(t, err, commerr.ErrConflict)
	assert.Nil(t, submission.Instance)
	stats, err := fixture.storage.GetStats(t.Context())
	require.NoError(t, err)
	assert.Zero(t, stats.ActiveWorkflowInstances)
}

func TestRootWorkflowSubmissionRetainsSchedulingFailure(t *testing.T) {
	t.Parallel()
	fixture, prepared, dispatch := newRootSubmissionFixture(t)
	input, err := dispatch.prepareRootSubmission(t.Context(), prepared)
	require.NoError(t, err)
	api := client.New(rootSubmissionTimeoutBackend{Backend: fixture.storage})
	submission, err := dispatch.submitRootWorkflow(t.Context(), api, input)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, input, submission.Input)
	assert.Nil(t, submission.Instance)
	stats, err := fixture.storage.GetStats(t.Context())
	require.NoError(t, err)
	assert.Zero(t, stats.ActiveWorkflowInstances)
}

func TestRootSubmissionRefusesMissingStorage(t *testing.T) {
	t.Parallel()
	fixture, prepared, _ := newRootSubmissionFixture(t)
	dispatch := newWorkflowDispatch(t.Context(), &Runtime{
		sessionID: prepared.lease.SessionID,
	}, uuid.New(), nil)
	_, err := dispatch.submitRoot(t.Context(), fixture.api, prepared)
	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
}

func TestRootSubmissionRefusesMissingDependencies(t *testing.T) {
	t.Parallel()
	dispatch := newWorkflowDispatch(context.WithoutCancel(t.Context()),
		&Runtime{}, uuid.New(), nil)
	_, err := dispatch.submitRoot(t.Context(), nil, nil)
	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
}
