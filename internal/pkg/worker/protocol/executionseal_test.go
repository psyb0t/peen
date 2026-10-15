package protocol

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rpcCompletionClaim(
	t *testing.T,
	fixture workflowRPCFixture,
	remote *Store,
	execution *models.Execution,
) (session.Lease, *session.ActivityClaim) {
	t.Helper()
	generationID := fixture.controller.generationID
	lease, err := remote.RestoreExecutionTurn(t.Context(), fixture.sessionID,
		execution.ID, session.RestoreExecutionTurnInput{
			OwnerGenerationID: generationID, WorkerGenerationID: generationID,
		})
	require.NoError(t, err)
	claim, err := remote.BeginExecutionActivity(t.Context(), fixture.sessionID,
		execution.ID, generationID, session.ScheduleActivityInput{
			StepID: "terminalHooks:0", Kind: models.ActivityKindTool,
			Name:         session.ExecutionTerminalHooksActivityName,
			ReplayPolicy: models.ActivityReplayPolicyReconcile,
			Input:        json.RawMessage(rpcExecutionContext),
		})
	require.NoError(t, err)

	return lease, claim
}

func TestCompletionSealRPCRepeatsWhilePausedWithoutMutation(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	_, claim := rpcCompletionClaim(t, fixture, remote, execution)
	first, err := remote.ReserveExecutionCompletion(t.Context(),
		fixture.sessionID, claim.Lease)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Equal(t, claim.Activity.ID, first.ActivityID)
	assert.Zero(t, first.InboxSequence)
	_, err = remote.PauseExecution(t.Context(), fixture.sessionID,
		execution.ID, "operator pause")
	require.NoError(t, err)
	before, err := remote.GetExecution(t.Context(), fixture.sessionID,
		execution.ID)
	require.NoError(t, err)
	repeated, err := remote.ReserveExecutionCompletion(t.Context(),
		fixture.sessionID, claim.Lease)
	require.NoError(t, err)
	assert.Equal(t, first, repeated)
	after, err := remote.GetExecution(t.Context(), fixture.sessionID,
		execution.ID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestCompletionSealRPCPendingInputReturnsNil(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	lease, claim := rpcCompletionClaim(t, fixture, remote, execution)
	_, err := remote.AcceptExecutionMessage(t.Context(), execution.ID, lease,
		session.AcceptExecutionMessageInput{
			RequestID: uuid.New(), WorkingDirectory: rpcExecutionWorkspace,
			Message: "continue retained work", Capacity: 1,
		})
	require.NoError(t, err)
	before, err := remote.GetExecution(t.Context(), fixture.sessionID,
		execution.ID)
	require.NoError(t, err)
	seal, err := remote.ReserveExecutionCompletion(t.Context(),
		fixture.sessionID, claim.Lease)
	require.NoError(t, err)
	assert.Nil(t, seal)
	after, err := remote.GetExecution(t.Context(), fixture.sessionID,
		execution.ID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestCompletionSealRPCRejectsInvalidClaims(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	_, claim := rpcCompletionClaim(t, fixture, remote, execution)
	wrongGeneration := claim.Lease
	wrongGeneration.GenerationID = uuid.New()
	wrongAttempt := claim.Lease
	wrongAttempt.Attempt++
	testCases := []struct {
		name      string
		sessionID uuid.UUID
		lease     session.ActivityLease
		wantErr   error
	}{
		{
			name: "foreign session", sessionID: uuid.New(),
			lease: claim.Lease, wantErr: commerr.ErrPermissionDenied,
		},
		{
			name: "foreign generation", sessionID: fixture.sessionID,
			lease: wrongGeneration, wantErr: commerr.ErrPermissionDenied,
		},
		{
			name: "missing lease", sessionID: fixture.sessionID,
			wantErr: commerr.ErrPermissionDenied,
		},
		{
			name: "stale attempt", sessionID: fixture.sessionID,
			lease: wrongAttempt, wantErr: commerr.ErrConflict,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			seal, err := remote.ReserveExecutionCompletion(t.Context(),
				tc.sessionID, tc.lease)
			require.ErrorIs(t, err, tc.wantErr)
			assert.Nil(t, seal)
		})
	}
}

func TestCompletionSealRPCRejectsReplacedWorker(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	_, claim := rpcCompletionClaim(t, fixture, remote, execution)
	_, err := fixture.controller.store.CreateWorkerGeneration(
		t.Context(), fixture.sessionID, session.CreateWorkerGenerationInput{
			ID: uuid.New(), Kind: "control", AgentSpace: "control",
			State: "ready", Workspace: rpcExecutionWorkspace,
			SocketPath:     "/example/replacement.sock",
			CredentialHash: "EXAMPLE-HASH",
		},
	)
	require.NoError(t, err)
	seal, err := remote.ReserveExecutionCompletion(t.Context(),
		fixture.sessionID, claim.Lease)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	assert.Nil(t, seal)
}
