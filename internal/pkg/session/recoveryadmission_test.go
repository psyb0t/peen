package session

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoveryAdmissionRetainsInputUntilCompletion(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	input := inboxTestInput()
	input.AfterTurn = true
	accepted, err := fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.NoError(t, err)
	assert.Equal(t, models.InboxMessageStateDeferred, accepted.Message.State)
	require.Len(t, accepted.Events, 2)
	assert.Equal(t, EventTypeUserMessageQueued, accepted.Events[1].EventType)
	repeated, err := fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.NoError(t, err)
	assert.Equal(t, accepted.Message.ID, repeated.Message.ID)
	assert.Empty(t, repeated.Events)
	queued, err := fixture.store.ListExecutionMessages(t.Context(),
		fixture.sessionID, fixture.root.ID, ListExecutionInboxOptions{
			State: models.InboxMessageStateQueued,
		})
	require.NoError(t, err)
	assert.Empty(t, queued.Items)
	_, err = fixture.store.AcquireDeferredExecutionTurn(t.Context(),
		fixture.sessionID, accepted.Message.ID, StartTurnInput{
			RequestID: input.RequestID, Workspace: input.WorkingDirectory,
		})
	require.ErrorIs(t, err, ErrSessionBusy)
	input.AfterTurn = false
	_, err = fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestRecoveryAdmissionStillRequiresCurrentLeaseAndRevision(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	input := inboxTestInput()
	input.AfterTurn = true
	forged := lease
	forged.ID = uuid.New()
	_, err := fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, forged, input)
	require.ErrorIs(t, err, ErrTurnLeaseInactive)
	stored, err := fixture.store.Get(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	revision := stored.StopRevision + 1
	input.ExpectedStopRevision = &revision
	_, err = fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	input.ExpectedStopRevision = nil
	_, err = fixture.store.PauseExecution(t.Context(), fixture.sessionID,
		fixture.root.ID, ExecutionUserStopReason)
	require.NoError(t, err)
	_, err = fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
}
