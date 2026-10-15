package session

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutionTurnLeaseSurvivesControllerReopen(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	input := RestoreExecutionTurnInput{OwnerGenerationID: uuid.New()}
	lease, err := fixture.store.RestoreExecutionTurn(t.Context(),
		fixture.sessionID, fixture.root.ID, input)
	require.NoError(t, err)
	repeated, err := fixture.store.RestoreExecutionTurn(t.Context(),
		fixture.sessionID, fixture.root.ID, input)
	require.NoError(t, err)
	assert.Equal(t, lease, repeated)
	before, err := fixture.store.ListExecutionEvents(t.Context(),
		fixture.sessionID, fixture.root.ID, ListEventsOptions{})
	require.NoError(t, err)
	assert.Len(t, before.Items, 2)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	reopened, err := NewStore(handle, Options{})
	require.NoError(t, err)
	_, err = reopened.RecoverInterrupted(t.Context())
	require.NoError(t, err)
	adopted, err := reopened.RestoreExecutionTurn(t.Context(),
		fixture.sessionID, fixture.root.ID, input)
	require.NoError(t, err)
	assert.Equal(t, lease, adopted)
	require.NoError(t, reopened.AppendCheckpoint(t.Context(), adopted,
		[]MessageInput{{Role: models.MessageRoleAssistant, Content: "continued"}},
		nil))
	turns, err := reopened.ListTurns(t.Context(), fixture.sessionID,
		ListTurnsOptions{})
	require.NoError(t, err)
	require.Len(t, turns.Items, 1)
	assert.Equal(t, fixture.turnID, turns.Items[0].ID)
	assert.Equal(t, models.TurnStateRunning, turns.Items[0].State)
}

func TestExecutionTurnLeaseReplacementRejectsStaleClaims(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	firstInput := RestoreExecutionTurnInput{OwnerGenerationID: uuid.New()}
	first, err := fixture.store.RestoreExecutionTurn(t.Context(),
		fixture.sessionID, fixture.root.ID, firstInput)
	require.NoError(t, err)
	secondInput := RestoreExecutionTurnInput{
		OwnerGenerationID: uuid.New(), PreviousLeaseID: first.ID,
	}
	second, err := fixture.store.RestoreExecutionTurn(t.Context(),
		fixture.sessionID, fixture.root.ID, secondInput)
	require.NoError(t, err)
	assert.Equal(t, first.TurnID, second.TurnID)
	assert.NotEqual(t, first.ID, second.ID)
	repeated, err := fixture.store.RestoreExecutionTurn(t.Context(),
		fixture.sessionID, fixture.root.ID, secondInput)
	require.NoError(t, err)
	assert.Equal(t, second, repeated)
	_, err = fixture.store.RestoreExecutionTurn(t.Context(),
		fixture.sessionID, fixture.root.ID, firstInput)
	require.ErrorIs(t, err, commerr.ErrConflict)
	require.ErrorIs(t, fixture.store.AppendCheckpoint(t.Context(), first,
		nil, nil), commerr.ErrInvalidState)
	fixture.store.ReleaseTurn(first)
	require.NoError(t, fixture.store.AppendCheckpoint(t.Context(), second,
		nil, nil))
}

func TestExecutionTurnLeasePreservesPausedState(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	input := RestoreExecutionTurnInput{OwnerGenerationID: uuid.New()}
	lease, err := fixture.store.RestoreExecutionTurn(t.Context(),
		fixture.sessionID, fixture.root.ID, input)
	require.NoError(t, err)
	_, err = fixture.store.PauseExecution(t.Context(), fixture.sessionID,
		fixture.root.ID, executionTestPauseReason)
	require.NoError(t, err)
	before, err := fixture.store.GetExecution(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	_, err = fixture.store.RestoreExecutionTurn(t.Context(),
		fixture.sessionID, fixture.root.ID, input)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	stored, err := fixture.store.GetExecutionTurnLease(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, lease.ID, stored.LeaseID)
	after, err := fixture.store.GetExecution(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, before.EventCount, after.EventCount)
	assert.Equal(t, models.ExecutionStatePaused, after.State)
}

func TestExecutionTurnLeaseRefusesUnrelatedActiveTurn(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	input := RestoreExecutionTurnInput{OwnerGenerationID: uuid.New()}
	lease, err := fixture.store.RestoreExecutionTurn(t.Context(),
		fixture.sessionID, fixture.root.ID, input)
	require.NoError(t, err)
	fixture.store.ReleaseTurn(lease)
	unrelated := startAgentRunTestTurn(t.Context(), t, fixture.store,
		fixture.sessionID)
	_, err = fixture.store.RestoreExecutionTurn(t.Context(),
		fixture.sessionID, fixture.root.ID, input)
	require.ErrorIs(t, err, ErrSessionBusy)
	require.NoError(t, fixture.store.AppendCheckpoint(t.Context(), unrelated,
		nil, nil))
	_, err = fixture.store.GetExecutionTurnLease(t.Context(), uuid.New(),
		fixture.root.ID)
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

func TestExecutionTurnLeaseRejectsOldRequestAfterSameWorkerResume(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	input := RestoreExecutionTurnInput{OwnerGenerationID: uuid.New()}
	first, err := fixture.store.RestoreExecutionTurn(t.Context(),
		fixture.sessionID, fixture.root.ID, input)
	require.NoError(t, err)
	_, err = fixture.store.PauseExecution(t.Context(), fixture.sessionID,
		fixture.root.ID, executionTestPauseReason)
	require.NoError(t, err)
	_, err = fixture.store.ResumeExecution(t.Context(), fixture.sessionID,
		fixture.root.ID)
	require.NoError(t, err)
	_, err = fixture.store.RestoreExecutionTurn(t.Context(), fixture.sessionID,
		fixture.root.ID, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	execution, err := fixture.store.GetExecution(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, execution.ControlRevision)
	input.ControlRevision = execution.ControlRevision
	input.PreviousLeaseID = first.ID
	resumed, err := fixture.store.RestoreExecutionTurn(t.Context(),
		fixture.sessionID, fixture.root.ID, input)
	require.NoError(t, err)
	assert.NotEqual(t, first.ID, resumed.ID)
	repeated, err := fixture.store.RestoreExecutionTurn(t.Context(),
		fixture.sessionID, fixture.root.ID, input)
	require.NoError(t, err)
	assert.Equal(t, resumed, repeated)
	require.ErrorIs(t, fixture.store.AppendCheckpoint(t.Context(), first,
		nil, nil), commerr.ErrInvalidState)
}

func TestExecutionTurnLeaseValidatesIdentityAndTerminalState(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		state   models.ExecutionState
		owner   uuid.UUID
		wantErr error
	}{
		{
			name: "missing owner", state: models.ExecutionStateQueued,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "completed", state: models.ExecutionStateCompleted,
			owner: uuid.New(), wantErr: commerr.ErrInvalidState,
		},
		{
			name: "failed", state: models.ExecutionStateFailed, owner: uuid.New(),
			wantErr: commerr.ErrInvalidState,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionFixture(t)
			repository := fixture.store.query.Execution
			_, err := repository.WithContext(t.Context()).
				Where(repository.ID.Eq(fixture.root.ID)).
				UpdateSimple(repository.State.Value(string(tc.state)))
			require.NoError(t, err)
			_, err = fixture.store.RestoreExecutionTurn(t.Context(),
				fixture.sessionID, fixture.root.ID,
				RestoreExecutionTurnInput{OwnerGenerationID: tc.owner})
			require.ErrorIs(t, err, tc.wantErr)
			_, err = fixture.store.GetExecutionTurnLease(t.Context(),
				fixture.sessionID, fixture.root.ID)
			require.ErrorIs(t, err, commerr.ErrNotFound)
		})
	}
}

func TestExecutionTurnLeaseRefusesChildAndNegativeRevision(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	input := RestoreExecutionTurnInput{OwnerGenerationID: uuid.New()}
	_, err := fixture.store.RestoreExecutionTurn(t.Context(), fixture.sessionID,
		child.ID, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	input.ControlRevision = -1
	_, err = fixture.store.RestoreExecutionTurn(t.Context(), fixture.sessionID,
		fixture.root.ID, input)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	_, err = fixture.store.GetExecutionTurnLease(t.Context(), fixture.sessionID,
		fixture.root.ID)
	require.ErrorIs(t, err, commerr.ErrNotFound)
}
