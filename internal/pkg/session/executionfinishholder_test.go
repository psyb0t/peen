package session

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutionFinishHolderCannotCompleteAfterReplacement(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	holder, err := fixture.store.GetExecutionTurnLease(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, holder.OwnerGenerationID,
		finishActivityTestInput())
	require.NoError(t, err)
	input := finishActivityTestResult()
	input.Finish.RootHolder = &ExecutionFinishHolder{
		LeaseID: lease.ID, ControlRevision: holder.ControlRevision,
	}
	_, err = fixture.store.RestoreExecutionTurn(t.Context(), fixture.sessionID,
		fixture.root.ID, RestoreExecutionTurnInput{
			OwnerGenerationID: uuid.New(), PreviousLeaseID: lease.ID,
		})
	require.NoError(t, err)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	execution, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateRunning, execution.State)
	activity, err := fixture.store.GetExecutionActivity(t.Context(),
		fixture.sessionID, claim.Activity.ID)
	require.NoError(t, err)
	assert.Nil(t, activity.OutputJSON)
}

func TestExecutionFinishHolderValidatesCurrentOwnership(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name       string
		change     func(*ExecutionFinishHolder)
		wrongOwner bool
		resume     bool
		wantErr    error
	}{
		{name: "current"},
		{
			name: "other lease", wantErr: commerr.ErrConflict,
			change: func(proof *ExecutionFinishHolder) { proof.LeaseID = uuid.New() },
		},
		{
			name: "other revision", wantErr: commerr.ErrConflict,
			change: func(proof *ExecutionFinishHolder) { proof.ControlRevision++ },
		},
		{
			name: "empty lease", wantErr: commerr.ErrValidationFailed,
			change: func(proof *ExecutionFinishHolder) { proof.LeaseID = uuid.Nil },
		},
		{
			name: "negative revision", wantErr: commerr.ErrValidationFailed,
			change: func(proof *ExecutionFinishHolder) { proof.ControlRevision = -1 },
		},
		{name: "other actor", wrongOwner: true, wantErr: commerr.ErrConflict},
		{name: "resumed execution", resume: true, wantErr: commerr.ErrConflict},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionFixture(t)
			lease := inboxTestLease(t, fixture)
			holder, err := fixture.store.GetExecutionTurnLease(t.Context(),
				fixture.sessionID, fixture.root.ID)
			require.NoError(t, err)
			owner := holder.OwnerGenerationID
			if tc.wrongOwner {
				owner = uuid.New()
			}

			claim, err := fixture.store.BeginExecutionActivity(t.Context(),
				fixture.sessionID, fixture.root.ID, owner, finishActivityTestInput())
			require.NoError(t, err)
			input := finishActivityTestResult()
			input.Finish.RootHolder = &ExecutionFinishHolder{
				LeaseID: lease.ID, ControlRevision: holder.ControlRevision,
			}
			if tc.change != nil {
				tc.change(input.Finish.RootHolder)
			}

			if tc.resume {
				_, err = fixture.store.PauseExecution(t.Context(), fixture.sessionID,
					fixture.root.ID, executionTestPauseReason)
				require.NoError(t, err)
				_, err = fixture.store.ResumeExecution(t.Context(), fixture.sessionID,
					fixture.root.ID)
				require.NoError(t, err)
			}

			_, err = fixture.store.CompleteExecutionActivity(t.Context(),
				fixture.sessionID, claim.Lease, input)
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}
