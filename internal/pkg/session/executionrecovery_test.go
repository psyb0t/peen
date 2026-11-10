package session

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	recoveryTestStepID       = "model:0"
	recoveryTestActivityName = "modelRound"
	crashedToolStepID        = "tool:1:0:outcomeHook"
)

func startExecutionForRecoveryTest(
	t *testing.T,
	store *Store,
	sessionID, executionID uuid.UUID,
) {
	t.Helper()
	_, err := store.BeginExecutionActivity(t.Context(), sessionID,
		executionID, uuid.New(), ScheduleActivityInput{
			StepID: recoveryTestStepID, Kind: models.ActivityKindModel,
			ReplayPolicy: models.ActivityReplayPolicyRetry,
			Name:         recoveryTestActivityName,
			Input:        json.RawMessage(emptyActivityJSON),
		})
	require.NoError(t, err)
}

func requireExecutionState(
	t *testing.T,
	store *Store,
	sessionID, executionID uuid.UUID,
	state models.ExecutionState,
) *models.Execution {
	t.Helper()
	stored, err := store.GetExecution(t.Context(), sessionID, executionID)
	require.NoError(t, err)
	assert.Equal(t, state, stored.State)

	return stored
}

func TestPauseUnfinishedExecutionsPausesRunningWorkInItsSession(
	t *testing.T,
) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	startExecutionForRecoveryTest(t, fixture.store, fixture.sessionID,
		fixture.root.ID)
	startExecutionForRecoveryTest(t, fixture.store, fixture.sessionID,
		child.ID)
	idle := newTestSession(t.Context(), t, fixture.store)
	idleLease := startAgentRunTestTurn(t.Context(), t, fixture.store,
		idle.ID)
	queued, err := fixture.store.CreateExecution(t.Context(), idle.ID,
		executionTestInput(idleLease.TurnID))
	require.NoError(t, err)
	other := newTestSession(t.Context(), t, fixture.store)
	otherLease := startAgentRunTestTurn(t.Context(), t, fixture.store,
		other.ID)
	otherRoot, err := fixture.store.CreateExecution(t.Context(), other.ID,
		executionTestInput(otherLease.TurnID))
	require.NoError(t, err)
	startExecutionForRecoveryTest(t, fixture.store, other.ID, otherRoot.ID)

	paused, err := fixture.store.PauseUnfinishedExecutions(t.Context(),
		fixture.sessionID)
	require.NoError(t, err)
	assert.Equal(t, 2, paused)

	for _, id := range []uuid.UUID{fixture.root.ID, child.ID} {
		stored := requireExecutionState(t, fixture.store, fixture.sessionID,
			id, models.ExecutionStatePaused)
		assert.Equal(t, ExecutionCrashPauseReason, stored.PauseReason)
		assert.NotNil(t, stored.PausedAt)
	}

	requireExecutionState(t, fixture.store, other.ID, otherRoot.ID,
		models.ExecutionStateRunning)

	paused, err = fixture.store.PauseUnfinishedExecutions(t.Context(),
		uuid.Nil)
	require.NoError(t, err)
	assert.Equal(t, 1, paused)
	requireExecutionState(t, fixture.store, other.ID, otherRoot.ID,
		models.ExecutionStatePaused)
	requireExecutionState(t, fixture.store, idle.ID, queued.ID,
		models.ExecutionStateQueued)
}

// A step a crash cut off whose effect cannot be repeated is uncertain as soon
// as the work is paused, so it can be resolved before anything resumes it. A
// step that is safe to repeat stays as it was and runs again on resume.
func TestPauseUnfinishedExecutionsMarksCrashedToolCallsUncertain(
	t *testing.T,
) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	tool := beginActivityForTest(t, fixture, uuid.New(),
		models.ActivityKindTool)
	startExecutionForRecoveryTest(t, fixture.store, fixture.sessionID,
		child.ID)

	paused, err := fixture.store.PauseUnfinishedExecutions(t.Context(),
		fixture.sessionID)
	require.NoError(t, err)
	assert.Equal(t, 2, paused)

	crashed, err := fixture.store.GetExecutionActivity(t.Context(),
		fixture.sessionID, tool.Activity.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ActivityStateUncertain, crashed.State)

	repeatable, err := fixture.store.ListExecutionActivities(t.Context(),
		fixture.sessionID, child.ID, ListEventsOptions{Limit: 1})
	require.NoError(t, err)
	require.Len(t, repeatable.Items, 1)
	assert.Equal(t, models.ActivityStateRunning, repeatable.Items[0].State)

	resolved, err := fixture.store.ResolveExecutionActivity(t.Context(),
		fixture.sessionID, tool.Activity.ID, ResolveActivityInput{
			Attempt: tool.Lease.Attempt, Resolution: ActivityResolutionRetry,
			Evidence: activityTestEvidence,
		})
	require.NoError(t, err)
	assert.Equal(t, models.ActivityStateScheduled, resolved.State)
	requireExecutionState(t, fixture.store, fixture.sessionID,
		fixture.root.ID, models.ExecutionStatePaused)
}

func TestResolvingASettledStepNamesTheUncertainOnes(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	settled := beginActivityForTest(t, fixture, uuid.New(),
		models.ActivityKindTool)
	_, err := fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, settled.Lease, activityTestResult())
	require.NoError(t, err)

	resolveSettled := func() error {
		_, err := fixture.store.ResolveExecutionActivity(t.Context(),
			fixture.sessionID, settled.Activity.ID, ResolveActivityInput{
				Attempt: settled.Lease.Attempt, Evidence: activityTestEvidence,
				Resolution: ActivityResolutionRetry,
			})

		return err
	}

	err = resolveSettled()
	require.ErrorIs(t, err, commerr.ErrConflict)
	assert.Contains(t, err.Error(), "no uncertain step")

	input := activityTestInput(models.ActivityKindTool)
	input.StepID = crashedToolStepID
	crashed, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, uuid.New(), input)
	require.NoError(t, err)
	_, err = fixture.store.PauseUnfinishedExecutions(t.Context(),
		fixture.sessionID)
	require.NoError(t, err)

	err = resolveSettled()
	require.ErrorIs(t, err, commerr.ErrConflict)
	assert.Contains(t, err.Error(), crashed.Activity.ID.String())
	assert.Contains(t, err.Error(), crashedToolStepID)
}
