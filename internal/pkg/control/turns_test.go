package control_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTurnRouterStopInvalidatesReservedMessage(t *testing.T) {
	fixture := newRoutingFixture(t)
	old := fixture.router.ReserveSessionMessage(fixture.sessionID)
	t.Cleanup(old.Release)
	stopped, err := fixture.router.CancelSessionTurn(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	assert.True(t, stopped)
	_, err = fixture.router.RunReservedSessionMessage(t.Context(), old,
		agent.MessageRequest{Message: routedMessage}, uuid.New())
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, fixture.launcher.workers)
	result, err := fixture.router.RunSessionMessage(t.Context(), fixture.sessionID,
		agent.MessageRequest{Message: routedMessage}, uuid.New())
	require.NoError(t, err)
	assert.Equal(t, routedAnswer, result.Text)
}

func TestTurnRouterCarriesStoredStopRevision(t *testing.T) {
	fixture := newRoutingFixture(t)
	_, err := fixture.store.Cancel(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	_, err = fixture.router.RunSessionMessage(t.Context(), fixture.sessionID,
		agent.MessageRequest{Message: routedMessage}, uuid.New())
	require.NoError(t, err)
	require.Len(t, fixture.launcher.workers, 1)
	turns := fixture.launcher.workers[0].turns
	require.Len(t, turns, 1)
	require.NotNil(t, turns[0].ExpectedStopRevision)
	assert.EqualValues(t, 1, *turns[0].ExpectedStopRevision)
}
