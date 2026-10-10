package session

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// admissionNotYet is how long a test waits to confirm a ticket is still held
// back. Admission is a channel close, so a ticket that should not be admitted
// would be within microseconds.
const admissionNotYet = 50 * time.Millisecond

func isAdmitted(ticket *AdmissionTicket) bool {
	ctx, cancel := context.WithTimeout(context.Background(), admissionNotYet)
	defer cancel()

	return ticket.Wait(ctx) == nil
}

// Tickets are admitted in the order they were reserved, one at a time.
func TestAdmissionGateAdmitsInReservationOrder(t *testing.T) {
	t.Parallel()

	gate := NewAdmissionGate()
	sessionID := uuid.New()

	first := gate.Reserve(sessionID)
	second := gate.Reserve(sessionID)
	third := gate.Reserve(sessionID)

	require.True(t, isAdmitted(first))
	assert.False(t, isAdmitted(second))
	assert.False(t, isAdmitted(third))

	first.Release()
	require.True(t, isAdmitted(second))
	assert.False(t, isAdmitted(third))

	second.Release()
	require.True(t, isAdmitted(third))

	third.Release()
	assert.Empty(t, gate.lines)
}

// A waiter that gives up leaves its place without holding up the ones behind
// it, and releasing a ticket twice does not admit anyone early.
func TestAdmissionGateDropsAbandonedTickets(t *testing.T) {
	t.Parallel()

	gate := NewAdmissionGate()
	sessionID := uuid.New()

	holder, err := gate.Acquire(context.Background(), sessionID)
	require.NoError(t, err)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = gate.Acquire(cancelled, sessionID)
	require.ErrorIs(t, err, context.Canceled)

	next := gate.Reserve(sessionID)
	last := gate.Reserve(sessionID)

	assert.False(t, isAdmitted(next))

	holder()
	holder()

	require.True(t, isAdmitted(next))
	assert.False(t, isAdmitted(last), "a double release admitted two tickets")

	next.Release()
	last.Release()
	assert.Empty(t, gate.lines)
}

// One session's line never holds back another session.
func TestAdmissionGateKeepsSessionsApart(t *testing.T) {
	t.Parallel()

	gate := NewAdmissionGate()
	otherSessionID := uuid.New()

	busy := gate.Reserve(uuid.New())
	t.Cleanup(busy.Release)

	other := gate.Reserve(otherSessionID)
	t.Cleanup(other.Release)

	assert.True(t, isAdmitted(other))
	assert.Equal(t, otherSessionID, other.SessionID())
}
