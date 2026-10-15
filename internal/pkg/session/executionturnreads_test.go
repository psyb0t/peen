package session

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutionRootTurnReadPreservesPausedWork(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	_, err := fixture.store.PauseExecution(t.Context(), fixture.sessionID,
		fixture.root.ID, "retain paused work")
	require.NoError(t, err)
	original, err := fixture.store.GetExecutionRootTurn(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, fixture.root.RootTurnID, original.ID)
	state, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, state.State)
	_, err = fixture.store.GetExecutionTurnLease(t.Context(), fixture.sessionID,
		fixture.root.ID)
	require.ErrorIs(t, err, commerr.ErrNotFound)
	_, err = fixture.store.GetExecutionRootTurn(t.Context(), uuid.New(),
		fixture.root.ID)
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

func TestExecutionRootTurnReadRejectsChild(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	_, err := fixture.store.GetExecutionRootTurn(t.Context(), fixture.sessionID,
		child.ID)
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestExecutionRootTurnReadRetainsTerminalTurn(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease, err := fixture.store.RestoreExecutionTurn(t.Context(),
		fixture.sessionID, fixture.root.ID,
		RestoreExecutionTurnInput{OwnerGenerationID: uuid.New()})
	require.NoError(t, err)
	require.NoError(t, fixture.store.FinalizeTurn(t.Context(), lease,
		FinalizeTurnInput{State: models.TurnStateCompleted}))
	original, err := fixture.store.GetExecutionRootTurn(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, models.TurnStateCompleted, original.State)
	assert.False(t, fixture.store.IsActive(fixture.sessionID))
}
