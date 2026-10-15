package session

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const receiptTestEventType = "turn.completed"

func TestFinalActivityReceiptPublishesOnlyCommittedEvents(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		pending   bool
		invalid   bool
		wantCount int
		wantErr   error
	}{
		{name: "terminal", wantCount: 1},
		{name: "continuation", pending: true},
		{
			name: "rollback", invalid: true,
			wantErr: commerr.ErrValidationFailed,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
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
			input.Finish.RootHolder = &ExecutionFinishHolder{LeaseID: lease.ID}
			input.Finish.Turn = &FinalizeTurnInput{
				State: models.TurnStateCompleted,
				Events: []EventInput{{
					ID:        uuid.New(),
					EventType: receiptTestEventType, PayloadJSON: `{}`,
				}},
			}
			if tc.pending {
				_, err = fixture.store.AcceptExecutionMessage(t.Context(),
					fixture.root.ID, lease, inboxTestInput())
				require.NoError(t, err)
				input.Finish.PendingOutput = json.RawMessage(`{"continue":true}`)
			}
			if tc.invalid {
				input.Finish.Turn.Events[0].PayloadJSON = "invalid"
			}
			receipt, err := fixture.store.CompleteExecutionActivityReceipt(
				t.Context(), fixture.sessionID, claim.Lease, input)
			require.ErrorIs(t, err, tc.wantErr)
			if tc.wantErr != nil {
				assert.Nil(t, receipt)
				assert.True(t, fixture.store.IsActive(fixture.sessionID))

				return
			}
			assert.Len(t, receipt.Events, tc.wantCount)
			repeated, err := fixture.store.CompleteExecutionActivityReceipt(
				t.Context(), fixture.sessionID, claim.Lease, input)
			require.NoError(t, err)
			assert.Empty(t, repeated.Events)
			assert.Equal(t, receipt.Activity, repeated.Activity)
		})
	}
}

func TestActivityCompletionWhilePausedIsSavedWithoutContinuation(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	claim := beginActivityForTest(t, fixture, uuid.New(), models.ActivityKindTool)
	_, err := fixture.store.PauseExecution(t.Context(),
		fixture.sessionID, fixture.root.ID, executionTestPauseReason)
	require.NoError(t, err)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, activityTestResult())
	require.NoError(t, err)
	paused := beginActivityForTest(t, fixture, uuid.New(), models.ActivityKindTool)
	assert.Equal(t, ActivityDecisionPaused, paused.Decision)
	_, err = fixture.store.ResumeExecution(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	reused := beginActivityForTest(t, fixture, uuid.New(), models.ActivityKindTool)
	assert.Equal(t, ActivityDecisionReuse, reused.Decision)
}

func TestInterruptedPausedModelRetriesOnlyAfterResume(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	generationID := uuid.New()
	claim := beginActivityForTest(t, fixture, generationID, models.ActivityKindModel)
	_, err := fixture.store.PauseExecution(t.Context(),
		fixture.sessionID, fixture.root.ID, executionTestPauseReason)
	require.NoError(t, err)
	require.NoError(t, fixture.store.InterruptExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease))
	paused := beginActivityForTest(t, fixture, generationID, models.ActivityKindModel)
	assert.Equal(t, ActivityDecisionPaused, paused.Decision)
	assert.Equal(t, models.ActivityStatePaused, paused.Activity.State)
	_, err = fixture.store.ResumeExecution(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	retry := beginActivityForTest(t, fixture, generationID, models.ActivityKindModel)
	assert.Equal(t, ActivityDecisionExecute, retry.Decision)
	assert.EqualValues(t, 2, retry.Lease.Attempt)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, activityTestResult())
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestPausedActivityDoesNotStartAnAttempt(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	_, err := fixture.store.PauseExecution(t.Context(),
		fixture.sessionID, fixture.root.ID, executionTestPauseReason)
	require.NoError(t, err)
	claim := beginActivityForTest(t, fixture, uuid.New(), models.ActivityKindModel)
	assert.Equal(t, ActivityDecisionPaused, claim.Decision)
	assert.Zero(t, claim.Activity.Attempt)
	assert.Empty(t, claim.Activity.OwnerGenerationID)
}
