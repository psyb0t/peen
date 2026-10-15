package session

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeChildCancellationSettlesExecutionAndFencesActivity(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	other := newTestSession(t.Context(), t, fixture.store)
	_, _, err := fixture.store.RequestAgentRunCancellation(t.Context(),
		other.ID, *child.AgentRunID)
	require.ErrorIs(t, err, commerr.ErrNotFound)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, uuid.New(), ScheduleActivityInput{
			StepID: "handler:0", Name: "cancelFixtureHandler",
			Kind:         models.ActivityKindTool,
			ReplayPolicy: models.ActivityReplayPolicyRetry,
			Input:        json.RawMessage(`{}`),
		})
	require.NoError(t, err)
	run, requested, err := fixture.store.RequestAgentRunCancellation(t.Context(),
		fixture.sessionID, *child.AgentRunID)
	require.NoError(t, err)
	assert.True(t, requested)
	assert.True(t, run.CancelRequested)
	assert.Equal(t, models.AgentRunStateCancelled, run.State)
	assert.NotNil(t, run.EndedAt)
	execution, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		child.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateCancelled, execution.State)
	assert.Equal(t, child.ContextJSON, execution.ContextJSON)
	assert.Equal(t, child.ControlRevision+1, execution.ControlRevision)
	activity, err := fixture.store.GetExecutionActivity(t.Context(),
		fixture.sessionID, claim.Activity.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ActivityStateCancelled, activity.State)
	assert.Equal(t, claim.Activity.InputJSON, activity.InputJSON)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, CompleteActivityInput{
			Output: json.RawMessage(`{}`), Context: json.RawMessage(`{}`),
		})
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestNativeChildCancellationRetainsSubtreeAndReport(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		state models.ExecutionState
	}{
		{name: "queued", state: models.ExecutionStateQueued},
		{name: "running", state: models.ExecutionStateRunning},
		{name: "paused", state: models.ExecutionStatePaused},
		{name: "uncertain", state: models.ExecutionStateReconciling},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionFixture(t)
			child := executionChildForTest(t, fixture, fixture.root)
			grandchild := executionChildForTest(t, fixture, child)
			sibling := executionChildForTest(t, fixture, fixture.root)
			repository := fixture.store.query.Execution
			_, err := repository.WithContext(t.Context()).Where(
				repository.ID.Eq(child.ID),
			).UpdateSimple(repository.State.Value(string(tc.state)))
			require.NoError(t, err)
			_, requested, err := fixture.store.RequestAgentRunCancellation(
				t.Context(), fixture.sessionID, *child.AgentRunID)
			require.NoError(t, err)
			assert.True(t, requested)
			for _, item := range []*models.Execution{child, grandchild} {
				stored, err := fixture.store.GetExecution(t.Context(),
					fixture.sessionID, item.ID)
				require.NoError(t, err)
				assert.Equal(t, models.ExecutionStateCancelled, stored.State)
				assert.Equal(t, item.ContextJSON, stored.ContextJSON)
				assert.NotNil(t, stored.CompletedAt)
			}
			for _, item := range []*models.Execution{fixture.root, sibling} {
				stored, err := fixture.store.GetExecution(t.Context(),
					fixture.sessionID, item.ID)
				require.NoError(t, err)
				assert.Equal(t, item.State, stored.State)
			}
			assertRetainedCancellationReport(t, fixture, child)
		})
	}
}

func assertRetainedCancellationReport(
	t *testing.T,
	fixture executionFixture,
	child *models.Execution,
) {
	t.Helper()
	before, err := fixture.store.GetAgentRun(t.Context(), fixture.sessionID,
		*child.AgentRunID)
	require.NoError(t, err)
	repeated, requested, err := fixture.store.RequestAgentRunCancellation(
		t.Context(), fixture.sessionID, before.ID)
	require.NoError(t, err)
	assert.False(t, requested)
	assert.Equal(t, before, repeated)
	page, err := fixture.store.ListExecutionMessages(t.Context(),
		fixture.sessionID, fixture.root.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	events, err := fixture.store.ListAgentRunEvents(t.Context(),
		fixture.sessionID, before.ID, ListAgentRunEventsOptions{})
	require.NoError(t, err)
	require.Len(t, events.Items, 1)
	assert.Equal(t, EventTypeAgentRunCancelled, events.Items[0].EventType)
	assert.Equal(t, &events.Items[0].ID, page.Items[0].SourceEventID)
	assert.Contains(t, page.Items[0].Message, before.ID.String())
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	reopened := newTestStore(t, handle)
	after, requested, err := reopened.RequestAgentRunCancellation(t.Context(),
		fixture.sessionID, before.ID)
	require.NoError(t, err)
	assert.False(t, requested)
	assert.Equal(t, before, after)
	retained, err := reopened.ListExecutionMessages(t.Context(),
		fixture.sessionID, fixture.root.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	assert.Equal(t, page, retained)
}

func TestNativeChildCancellationRollsBackExhaustedRevision(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	grandchild := executionChildForTest(t, fixture, child)
	repository := fixture.store.query.Execution
	_, err := repository.WithContext(t.Context()).Where(
		repository.ID.Eq(grandchild.ID),
	).UpdateSimple(repository.ControlRevision.Value(math.MaxInt64))
	require.NoError(t, err)
	_, _, err = fixture.store.RequestAgentRunCancellation(t.Context(),
		fixture.sessionID, *child.AgentRunID)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	for _, item := range []*models.Execution{child, grandchild} {
		stored, err := fixture.store.GetExecution(t.Context(),
			fixture.sessionID, item.ID)
		require.NoError(t, err)
		assert.Equal(t, item.State, stored.State)
		run, err := fixture.store.GetAgentRun(t.Context(), fixture.sessionID,
			*item.AgentRunID)
		require.NoError(t, err)
		assert.False(t, run.CancelRequested)
		assert.Nil(t, run.EndedAt)
	}
}

func TestNativeChildCancellationPreservesTerminalDescendants(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		state models.ExecutionState
	}{
		{name: "completed", state: models.ExecutionStateCompleted},
		{name: "failed", state: models.ExecutionStateFailed},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionFixture(t)
			child := executionChildForTest(t, fixture, fixture.root)
			finished := executionChildForTest(t, fixture, child)
			originalRun, err := fixture.store.FinalizeAgentRun(t.Context(),
				fixture.sessionID, *finished.AgentRunID, FinalizeAgentRunInput{
					State: models.AgentRunState(tc.state),
				})
			require.NoError(t, err)
			repository := fixture.store.query.Execution
			_, err = repository.WithContext(t.Context()).Where(
				repository.ID.Eq(finished.ID),
			).UpdateSimple(repository.State.Value(string(tc.state)))
			require.NoError(t, err)
			before, err := fixture.store.GetExecution(t.Context(),
				fixture.sessionID, finished.ID)
			require.NoError(t, err)
			_, _, err = fixture.store.RequestAgentRunCancellation(t.Context(),
				fixture.sessionID, *child.AgentRunID)
			require.NoError(t, err)
			after, err := fixture.store.GetExecution(t.Context(),
				fixture.sessionID, finished.ID)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			savedRun, err := fixture.store.GetAgentRun(t.Context(),
				fixture.sessionID, *finished.AgentRunID)
			require.NoError(t, err)
			assert.Equal(t, originalRun, savedRun)
		})
	}
}
