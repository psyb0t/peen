package session

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCancelPendingWaitsForPreparationCleanup(t *testing.T) {
	t.Parallel()

	gate := NewAdmissionGate()
	sessionID := uuid.New()
	first := gate.Reserve(sessionID)
	firstContext, cancelFirst := first.BindContext(context.Background())
	defer cancelFirst()
	second := gate.Reserve(sessionID)
	secondContext, cancelSecond := second.BindContext(context.Background())
	defer cancelSecond()

	require.Equal(t, 2, gate.CancelPending(sessionID))
	require.ErrorIs(t, firstContext.Err(), context.Canceled)
	require.ErrorIs(t, secondContext.Err(), context.Canceled)
	require.Equal(t, 0, gate.CancelPending(sessionID))
	fresh := gate.Reserve(sessionID)
	defer fresh.Release()
	assert.False(t, isAdmitted(fresh))
	first.Release()
	assert.False(t, isAdmitted(fresh))
	second.Release()
	require.NoError(t, fresh.Wait(context.Background()))
	first.Release()
}

func TestCancelPendingDropsUnstartedTickets(t *testing.T) {
	t.Parallel()

	gate := NewAdmissionGate()
	sessionID := uuid.New()
	unstarted := gate.Reserve(sessionID)
	other := gate.Reserve(uuid.New())
	defer other.Release()
	require.Equal(t, 1, gate.CancelPending(sessionID))
	lateContext, cancel := unstarted.BindContext(context.Background())
	defer cancel()
	require.ErrorIs(t, lateContext.Err(), context.Canceled)
	require.ErrorIs(t, unstarted.Wait(context.Background()), context.Canceled)
	fresh := gate.Reserve(sessionID)
	defer fresh.Release()
	require.NoError(t, fresh.Wait(context.Background()))
	require.NoError(t, other.Wait(context.Background()))
	unstarted.Release()
}

func TestReleasedAdmissionPreservesBoundContext(t *testing.T) {
	t.Parallel()

	gate := NewAdmissionGate()
	sessionID := uuid.New()
	ticket := gate.Reserve(sessionID)
	bound, cancel := ticket.BindContext(context.Background())
	defer cancel()
	ticket.Release()
	require.Zero(t, gate.CancelPending(sessionID))
	require.NoError(t, bound.Err())
}
