package session

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	recoveryTestStepID       = "model:0"
	recoveryTestActivityName = "modelRound"
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
