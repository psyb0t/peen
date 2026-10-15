package protocol

import (
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutionTurnRestorationRegistersColdLocalLease(t *testing.T) {
	t.Parallel()
	fixture, _, execution := newExecutionRPCFixture(t)
	cold := NewStore(fixture.remote.conn, fixture.sessionID,
		fixture.controller.generationID)
	lease, err := cold.RestoreExecutionTurn(t.Context(), fixture.sessionID,
		execution.ID, session.RestoreExecutionTurnInput{
			OwnerGenerationID:  fixture.controller.generationID,
			WorkerGenerationID: fixture.controller.generationID,
		})
	require.NoError(t, err)
	assert.True(t, cold.IsActive(fixture.sessionID))
	var cancelled atomic.Bool
	cold.RegisterCancellation(lease, func() { cancelled.Store(true) })
	assert.True(t, cold.RequestLocalCancellation(fixture.sessionID))
	assert.True(t, cancelled.Load())
	cold.ReleaseTurn(lease)
	assert.False(t, cold.IsActive(fixture.sessionID))
	assert.False(t, fixture.controller.store.IsActive(fixture.sessionID))
}

func TestExecutionTurnRestorationFencesStaleLocalCallbacks(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	input := session.RestoreExecutionTurnInput{
		OwnerGenerationID:  fixture.controller.generationID,
		WorkerGenerationID: fixture.controller.generationID,
	}
	before, err := remote.RestoreExecutionTurn(t.Context(), fixture.sessionID,
		execution.ID, input)
	require.NoError(t, err)
	var oldCancelled atomic.Bool
	remote.RegisterCancellation(before, func() { oldCancelled.Store(true) })
	_, err = remote.PauseExecution(t.Context(), fixture.sessionID,
		execution.ID, rpcPauseReason)
	require.NoError(t, err)
	_, err = remote.ResumeExecution(t.Context(), fixture.sessionID, execution.ID)
	require.NoError(t, err)
	input.PreviousLeaseID = before.ID
	input.ControlRevision = 2
	after, err := remote.RestoreExecutionTurn(t.Context(), fixture.sessionID,
		execution.ID, input)
	require.NoError(t, err)
	require.NotEqual(t, before.ID, after.ID)
	assert.True(t, oldCancelled.Load())
	var currentCancelled, staleCancelled atomic.Bool
	remote.RegisterCancellation(after, func() { currentCancelled.Store(true) })
	remote.RegisterCancellation(before, func() { staleCancelled.Store(true) })
	remote.ReleaseTurn(before)
	assert.True(t, remote.IsActive(fixture.sessionID))
	require.NoError(t, remote.AppendCheckpoint(t.Context(), after, nil, nil))
	assert.True(t, remote.RequestLocalCancellation(fixture.sessionID))
	assert.True(t, currentCancelled.Load())
	assert.False(t, staleCancelled.Load())
	remote.ReleaseTurn(after)
	assert.False(t, fixture.controller.store.IsActive(fixture.sessionID))
}

func TestExecutionTurnLeaseRestoresOverRPC(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	original, err := remote.GetExecutionRootTurn(t.Context(), fixture.sessionID,
		execution.ID)
	require.NoError(t, err)
	assert.Equal(t, execution.RootTurnID, original.ID)
	_, err = remote.GetExecutionRootTurn(t.Context(), uuid.New(), execution.ID)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	input := session.RestoreExecutionTurnInput{
		OwnerGenerationID:  fixture.controller.generationID,
		WorkerGenerationID: fixture.controller.generationID,
	}
	lease, err := remote.RestoreExecutionTurn(t.Context(), fixture.sessionID,
		execution.ID, input)
	require.NoError(t, err)
	assert.Equal(t, execution.RootTurnID, lease.TurnID)
	stored, err := remote.GetExecutionTurnLease(t.Context(), fixture.sessionID,
		execution.ID)
	require.NoError(t, err)
	assert.Equal(t, lease.ID, stored.LeaseID)
	assert.Equal(t, input.OwnerGenerationID, stored.OwnerGenerationID)
	var cancelled atomic.Bool
	remote.RegisterCancellation(lease, func() { cancelled.Store(true) })
	repeated, err := remote.RestoreExecutionTurn(t.Context(), fixture.sessionID,
		execution.ID, input)
	require.NoError(t, err)
	assert.Equal(t, lease, repeated)
	assert.False(t, cancelled.Load())
	assert.True(t, remote.RequestLocalCancellation(fixture.sessionID))
	assert.True(t, cancelled.Load())
	require.NoError(t, remote.AppendCheckpoint(t.Context(), lease, nil, nil))
	_, err = remote.RestoreExecutionTurn(t.Context(), uuid.New(),
		execution.ID, input)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
}

func TestExecutionTurnLeaseRejectsForgedOwnersOverRPC(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name       string
		forgeOwner bool
	}{
		{name: "owner", forgeOwner: true},
		{name: "worker"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, remote, execution := newExecutionRPCFixture(t)
			input := session.RestoreExecutionTurnInput{
				OwnerGenerationID:  fixture.controller.generationID,
				WorkerGenerationID: fixture.controller.generationID,
			}
			if tc.forgeOwner {
				input.OwnerGenerationID = uuid.New()
			} else {
				input.WorkerGenerationID = uuid.New()
			}
			_, err := remote.RestoreExecutionTurn(t.Context(), fixture.sessionID,
				execution.ID, input)
			require.ErrorIs(t, err, commerr.ErrPermissionDenied)
			_, err = remote.GetExecutionTurnLease(t.Context(), fixture.sessionID,
				execution.ID)
			require.ErrorIs(t, err, commerr.ErrNotFound)
		})
	}
}

func TestExecutionTurnLeaseRejectsReplacedWorkerOverRPC(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	input := session.RestoreExecutionTurnInput{
		OwnerGenerationID:  fixture.controller.generationID,
		WorkerGenerationID: fixture.controller.generationID,
	}
	lease, err := remote.RestoreExecutionTurn(t.Context(), fixture.sessionID,
		execution.ID, input)
	require.NoError(t, err)
	_, err = fixture.controller.store.CreateWorkerGeneration(t.Context(),
		fixture.sessionID, session.CreateWorkerGenerationInput{
			ID: uuid.New(), Kind: "control", AgentSpace: "control",
			State: "ready", Workspace: "/workspace",
			SocketPath: "/example/replacement.sock", CredentialHash: "EXAMPLE-HASH",
		})
	require.NoError(t, err)
	_, err = remote.RestoreExecutionTurn(t.Context(), fixture.sessionID,
		execution.ID, input)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	_, err = remote.GetExecutionTurnLease(t.Context(), fixture.sessionID,
		execution.ID)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	stored, err := fixture.controller.store.GetExecutionTurnLease(t.Context(),
		fixture.sessionID, execution.ID)
	require.NoError(t, err)
	assert.Equal(t, lease.ID, stored.LeaseID)
}
