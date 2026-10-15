package session

import (
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutionChildClaimRecoversWithActivity(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name        string
		interrupted bool
	}{
		{name: "stale running owner"},
		{name: "interrupted owner", interrupted: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionFixture(t)
			child := executionChildForTest(t, fixture, fixture.root)
			before, err := fixture.store.GetAgentRun(t.Context(),
				fixture.sessionID, *child.AgentRunID)
			require.NoError(t, err)
			if tc.interrupted {
				_, err = fixture.store.RecoverInterruptedAgentRuns(t.Context())
				require.NoError(t, err)
			}

			generationID := uuid.New()
			claim, err := fixture.store.BeginExecutionActivity(t.Context(),
				fixture.sessionID, child.ID, generationID,
				activityTestInput(models.ActivityKindModel))
			require.NoError(t, err)
			assert.Equal(t, ActivityDecisionExecute, claim.Decision)
			after, err := fixture.store.GetAgentRun(t.Context(),
				fixture.sessionID, *child.AgentRunID)
			require.NoError(t, err)
			assert.Equal(t, models.AgentRunStateRunning, after.State)
			assert.Equal(t, generationID.String(), after.WorkerGenerationID)
			assert.Nil(t, after.EndedAt)
			assert.Equal(t, before.ID, after.ID)
			assert.Equal(t, before.StartedAt, after.StartedAt)
			assert.Equal(t, before.ParentTurnID, after.ParentTurnID)
		})
	}
}

func TestExecutionChildClaimPausePreservesInterruptedOwner(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	_, err := fixture.store.RecoverInterruptedAgentRuns(t.Context())
	require.NoError(t, err)
	_, err = fixture.store.PauseExecution(t.Context(), fixture.sessionID,
		child.ID, executionTestPauseReason)
	require.NoError(t, err)
	before, err := fixture.store.GetAgentRun(t.Context(), fixture.sessionID,
		*child.AgentRunID)
	require.NoError(t, err)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, uuid.New(),
		activityTestInput(models.ActivityKindModel))
	require.NoError(t, err)
	assert.Equal(t, ActivityDecisionPaused, claim.Decision)
	after, err := fixture.store.GetAgentRun(t.Context(), fixture.sessionID,
		*child.AgentRunID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestExecutionChildClaimFencesReplacedWorker(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	input := activityTestInput(models.ActivityKindModel)
	first, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, uuid.New(), input)
	require.NoError(t, err)
	owner := uuid.New()
	second, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, owner, input)
	require.NoError(t, err)
	assert.EqualValues(t, 2, second.Lease.Attempt)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, first.Lease, activityTestResult())
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, second.Lease, activityTestResult())
	require.NoError(t, err)

	input.StepID = "nextModel"
	_, err = fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, owner, input)
	require.NoError(t, err)
	run, err := fixture.store.GetAgentRun(t.Context(), fixture.sessionID,
		*child.AgentRunID)
	require.NoError(t, err)
	assert.Equal(t, owner.String(), run.WorkerGenerationID)
	assert.Equal(t, models.AgentRunStateRunning, run.State)
}

func TestExecutionChildClaimRollsBackOnEventOverflow(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	owner := uuid.New()
	first, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, owner,
		activityTestInput(models.ActivityKindModel))
	require.NoError(t, err)
	_, err = fixture.store.RecoverInterruptedAgentRuns(t.Context())
	require.NoError(t, err)
	before, err := fixture.store.GetAgentRun(t.Context(), fixture.sessionID,
		*child.AgentRunID)
	require.NoError(t, err)
	repository := repositories.Use(fixture.handle.GormDB).Execution
	_, err = repository.WithContext(t.Context()).Where(
		repository.ID.Eq(child.ID),
	).UpdateSimple(repository.EventCount.Value(math.MaxInt64))
	require.NoError(t, err)
	_, err = fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, uuid.New(),
		activityTestInput(models.ActivityKindModel))
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	after, err := fixture.store.GetAgentRun(t.Context(), fixture.sessionID,
		*child.AgentRunID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	retained, err := fixture.store.GetExecutionActivity(t.Context(),
		fixture.sessionID, first.Activity.ID)
	require.NoError(t, err)
	assert.Equal(t, first.Activity, retained)
}

func TestExecutionChildClaimDoesNotReviveStoppedWork(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		state     models.AgentRunState
		cancelled bool
		wantErr   error
	}{
		{name: "cancel requested", cancelled: true, wantErr: commerr.ErrInvalidState},
		{name: "completed", state: models.AgentRunStateCompleted, wantErr: commerr.ErrConflict},
		{name: "failed", state: models.AgentRunStateFailed, wantErr: commerr.ErrConflict},
		{name: "cancelled", state: models.AgentRunStateCancelled, wantErr: commerr.ErrConflict},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionFixture(t)
			child := executionChildForTest(t, fixture, fixture.root)
			if tc.cancelled {
				_, _, err := fixture.store.RequestAgentRunCancellation(t.Context(),
					fixture.sessionID, *child.AgentRunID)
				require.NoError(t, err)
			} else {
				_, err := fixture.store.FinalizeAgentRun(t.Context(),
					fixture.sessionID, *child.AgentRunID,
					FinalizeAgentRunInput{State: tc.state})
				require.NoError(t, err)
			}
			before, err := fixture.store.GetAgentRun(t.Context(),
				fixture.sessionID, *child.AgentRunID)
			require.NoError(t, err)

			_, err = fixture.store.BeginExecutionActivity(t.Context(),
				fixture.sessionID, child.ID, uuid.New(),
				activityTestInput(models.ActivityKindModel))
			require.ErrorIs(t, err, tc.wantErr)
			after, err := fixture.store.GetAgentRun(t.Context(),
				fixture.sessionID, *child.AgentRunID)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			page, err := fixture.store.ListExecutionActivities(t.Context(),
				fixture.sessionID, child.ID, ListEventsOptions{})
			require.NoError(t, err)
			assert.Empty(t, page.Items)
		})
	}
}
