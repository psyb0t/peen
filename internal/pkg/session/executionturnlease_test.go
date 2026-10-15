package session

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRestoredRootAdoptsExplicitlyResumedStopRevision(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	_, err := fixture.store.Cancel(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	fixture.store.ReleaseTurn(lease)
	_, err = fixture.store.ResumeExecution(t.Context(), fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	stored, err := fixture.store.GetExecution(t.Context(), fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	_, err = fixture.store.RestoreExecutionTurn(t.Context(), fixture.sessionID, fixture.root.ID, RestoreExecutionTurnInput{
		OwnerGenerationID: uuid.New(), PreviousLeaseID: lease.ID, ControlRevision: stored.ControlRevision,
	})
	require.NoError(t, err)
	turn, err := fixture.store.GetExecutionRootTurn(t.Context(), fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	current, err := fixture.store.Get(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	assert.Positive(t, current.StopRevision)
	assert.Equal(t, current.StopRevision, turn.StopRevision)
	child := executionChildForTest(t, fixture, stored)
	assert.Equal(t, models.ExecutionStateQueued, child.State)
}
