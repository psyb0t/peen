package session

import (
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func completionSealClaim(
	t *testing.T,
	fixture executionFixture,
) *ActivityClaim {
	t.Helper()
	generationID := uuid.New()
	inputHolder := RestoreExecutionTurnInput{OwnerGenerationID: generationID}
	holder, err := fixture.store.GetExecutionTurnLease(t.Context(),
		fixture.sessionID, fixture.root.ID)
	if err != nil {
		require.ErrorIs(t, err, commerr.ErrNotFound)
	}
	if holder != nil {
		inputHolder.PreviousLeaseID = holder.LeaseID
	}

	_, err = fixture.store.RestoreExecutionTurn(t.Context(), fixture.sessionID,
		fixture.root.ID, inputHolder)
	require.NoError(t, err)
	input := activityTestInput(models.ActivityKindTool)
	input.StepID = "terminalHooks:0"
	input.Name = ExecutionTerminalHooksActivityName
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, generationID, input)
	require.NoError(t, err)

	return claim
}

func TestExecutionSealRetainsOneReservationAfterReopenAndPause(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	claim := completionSealClaim(t, fixture)
	first, err := fixture.store.ReserveExecutionCompletion(t.Context(),
		fixture.sessionID, claim.Lease)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Equal(t, claim.Activity.ID, first.ActivityID)
	assert.Zero(t, first.InboxSequence)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store := newTestStore(t, handle)
	_, err = store.PauseExecution(t.Context(), fixture.sessionID,
		fixture.root.ID, executionTestPauseReason)
	require.NoError(t, err)
	before, err := store.GetExecution(t.Context(), fixture.sessionID,
		fixture.root.ID)
	require.NoError(t, err)
	repeated, err := store.ReserveExecutionCompletion(t.Context(),
		fixture.sessionID, claim.Lease)
	require.NoError(t, err)
	assert.Equal(t, first, repeated)
	after, err := store.GetExecution(t.Context(), fixture.sessionID,
		fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestExecutionSealRefusesPendingAcceptedInput(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	_, err := fixture.store.AcceptExecutionMessage(t.Context(), fixture.root.ID,
		lease, inboxTestInput())
	require.NoError(t, err)
	claim := completionSealClaim(t, fixture)
	before, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		fixture.root.ID)
	require.NoError(t, err)
	seal, err := fixture.store.ReserveExecutionCompletion(t.Context(),
		fixture.sessionID, claim.Lease)
	require.NoError(t, err)
	assert.Nil(t, seal)
	after, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestExecutionSealRejectsForeignAndStaleClaims(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	claim := completionSealClaim(t, fixture)
	wrongGeneration := claim.Lease
	wrongGeneration.GenerationID = uuid.New()
	wrongAttempt := claim.Lease
	wrongAttempt.Attempt++
	testCases := []struct {
		name      string
		sessionID uuid.UUID
		lease     ActivityLease
		wantErr   error
	}{
		{
			name: "foreign session", sessionID: uuid.New(), lease: claim.Lease,
			wantErr: commerr.ErrNotFound,
		},
		{
			name: "missing lease", sessionID: fixture.sessionID,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "foreign worker", sessionID: fixture.sessionID,
			lease: wrongGeneration, wantErr: commerr.ErrPermissionDenied,
		},
		{
			name: "stale attempt", sessionID: fixture.sessionID,
			lease: wrongAttempt, wantErr: commerr.ErrConflict,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			seal, err := fixture.store.ReserveExecutionCompletion(t.Context(),
				tc.sessionID, tc.lease)
			require.ErrorIs(t, err, tc.wantErr)
			assert.Nil(t, seal)
		})
	}
}

func TestExecutionSealRejectsReplacedTurnHolder(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	claim := completionSealClaim(t, fixture)
	holder, err := fixture.store.GetExecutionTurnLease(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	_, err = fixture.store.RestoreExecutionTurn(t.Context(), fixture.sessionID,
		fixture.root.ID, RestoreExecutionTurnInput{
			OwnerGenerationID: uuid.New(), PreviousLeaseID: holder.LeaseID,
		})
	require.NoError(t, err)
	seal, err := fixture.store.ReserveExecutionCompletion(t.Context(),
		fixture.sessionID, claim.Lease)
	require.ErrorIs(t, err, commerr.ErrConflict)
	assert.Nil(t, seal)
}

func TestExecutionSealRejectsEventCounterExhaustion(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	claim := completionSealClaim(t, fixture)
	repository := fixture.store.query.Execution
	_, err := repository.WithContext(t.Context()).Where(
		repository.ID.Eq(fixture.root.ID),
	).UpdateSimple(repository.EventCount.Value(math.MaxInt64))
	require.NoError(t, err)
	seal, err := fixture.store.ReserveExecutionCompletion(t.Context(),
		fixture.sessionID, claim.Lease)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	assert.Nil(t, seal)
}

func TestExecutionSealRejectsAnotherActivityInput(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	claim := completionSealClaim(t, fixture)
	_, err := fixture.store.ReserveExecutionCompletion(t.Context(),
		fixture.sessionID, claim.Lease)
	require.NoError(t, err)
	repository := fixture.store.query.ExecutionActivity
	_, err = repository.WithContext(t.Context()).Where(
		repository.ID.Eq(claim.Activity.ID),
	).UpdateSimple(repository.InputJSON.Value(`{"changed":true}`))
	require.NoError(t, err)
	seal, err := fixture.store.ReserveExecutionCompletion(t.Context(),
		fixture.sessionID, claim.Lease)
	require.ErrorIs(t, err, commerr.ErrConflict)
	assert.Nil(t, seal)
}

func TestExecutionSealRejectsOrdinaryTool(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	claim := beginActivityForTest(t, fixture, uuid.New(),
		models.ActivityKindTool)
	seal, err := fixture.store.ReserveExecutionCompletion(t.Context(),
		fixture.sessionID, claim.Lease)
	require.ErrorIs(t, err, commerr.ErrConflict)
	assert.Nil(t, seal)
}

func TestExecutionSealRejectsUnfinishedOtherActivity(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	claim := completionSealClaim(t, fixture)
	input := activityTestInput(models.ActivityKindModel)
	input.StepID = "unfinishedModel"
	_, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, claim.Lease.GenerationID, input)
	require.NoError(t, err)
	seal, err := fixture.store.ReserveExecutionCompletion(t.Context(),
		fixture.sessionID, claim.Lease)
	require.ErrorIs(t, err, commerr.ErrConflict)
	assert.Nil(t, seal)
}

func TestExecutionSealRetainsDeliveredInboxWatermark(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	accepted := inboxTestInput()
	_, err := fixture.store.AcceptExecutionMessage(t.Context(), fixture.root.ID,
		lease, accepted)
	require.NoError(t, err)
	requestIDs := []uuid.UUID{accepted.RequestID}
	model := inboxTestClaim(t, fixture, requestIDs)
	_, err = fixture.store.DeliverExecutionMessages(t.Context(), fixture.sessionID,
		DeliverExecutionMessagesInput{
			TurnLease: lease, ModelLease: model.Lease, RequestIDs: requestIDs,
		})
	require.NoError(t, err)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		model.Lease, activityTestResult())
	require.NoError(t, err)
	claim := completionSealClaim(t, fixture)
	seal, err := fixture.store.ReserveExecutionCompletion(t.Context(),
		fixture.sessionID, claim.Lease)
	require.NoError(t, err)
	require.NotNil(t, seal)
	assert.Equal(t, int64(1), seal.InboxSequence)
}
