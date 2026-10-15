package session

import (
	"encoding/json"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutionFinalCheckpointAlsoFinishesOriginalTurn(t *testing.T) {
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
	input.Finish.Turn = &FinalizeTurnInput{State: models.TurnStateCompleted}
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	turn, err := fixture.store.GetExecutionRootTurn(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, models.TurnStateCompleted, turn.State)
	assert.NotNil(t, turn.CompletedAt)
	assert.False(t, fixture.store.IsActive(lease.SessionID))
	before, err := fixture.store.Get(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	after, err := fixture.store.Get(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store := newTestStore(t, handle)
	_, err = store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	input.Finish.Turn.Messages = []MessageInput{{
		Role: models.MessageRoleAssistant, Content: "changed final answer",
	}}
	_, err = store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestExecutionFinishTurnRollsBackInvalidTranscript(t *testing.T) {
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
		State:  models.TurnStateCompleted,
		Events: []EventInput{{PayloadJSON: "invalid JSON"}},
	}
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	activity, err := fixture.store.GetExecutionActivity(t.Context(),
		fixture.sessionID, claim.Activity.ID)
	require.NoError(t, err)
	assert.Nil(t, activity.OutputJSON)
	assert.Equal(t, models.ActivityStateRunning, activity.State)
	turn, err := fixture.store.GetExecutionRootTurn(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, models.TurnStateRunning, turn.State)
	assert.True(t, fixture.store.IsActive(lease.SessionID))
	input.Finish.Turn.Events = nil
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
}

func TestExecutionFinishContinuationRetainsOriginalTurn(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	holder, err := fixture.store.GetExecutionTurnLease(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	_, err = fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, inboxTestInput())
	require.NoError(t, err)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, holder.OwnerGenerationID,
		finishActivityTestInput())
	require.NoError(t, err)
	input := finishActivityTestResult()
	input.Finish.RootHolder = &ExecutionFinishHolder{LeaseID: lease.ID}
	input.Finish.Turn = &FinalizeTurnInput{State: models.TurnStateCompleted}
	input.Finish.PendingOutput = json.RawMessage(`{"continue":true}`)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	turn, err := fixture.store.GetExecutionRootTurn(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, models.TurnStateRunning, turn.State)
	assert.True(t, fixture.store.IsActive(lease.SessionID))
}
