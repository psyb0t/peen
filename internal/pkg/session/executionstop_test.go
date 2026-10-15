package session

import (
	"testing"

	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionStopPersistsExecutionPause(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	fixture.store.ReleaseTurn(inboxTestLease(t, fixture))
	before, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		fixture.root.ID)
	require.NoError(t, err)
	_, err = fixture.store.Cancel(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	stored, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, stored.State)
	assert.NotNil(t, stored.PausedAt)
	assert.Equal(t, before.EventCount+1, stored.EventCount)
	assert.Equal(t, fixture.root.ContextJSON, stored.ContextJSON)
}

func TestLateExecutionCreationRemainsPausedAfterSessionStop(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	fixture.store.ReleaseTurn(inboxTestLease(t, fixture))
	lease := startAgentRunTestTurn(t.Context(), t, fixture.store,
		fixture.sessionID)
	fixture.store.ReleaseTurn(lease)
	_, err := fixture.store.Cancel(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	input := executionTestInput(lease.TurnID)
	stored, err := fixture.store.CreateExecution(t.Context(),
		fixture.sessionID, input)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, stored.State)
	assert.NotNil(t, stored.PausedAt)
	assert.Equal(t, string(input.Context), stored.ContextJSON)
	assert.EqualValues(t, 2, stored.EventCount)
}
