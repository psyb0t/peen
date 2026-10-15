package session

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChildExecutionFinishCommitsRunWithActivity(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, uuid.New(), finishActivityTestInput())
	require.NoError(t, err)
	input := finishActivityTestResult()
	input.Finish.Child = &ExecutionFinishChild{
		State: models.AgentRunStateCompleted, ResponseText: "child response",
		ResponseMessagesJSON: `[]`,
	}
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	run, err := fixture.store.GetAgentRun(t.Context(), fixture.sessionID,
		*child.AgentRunID)
	require.NoError(t, err)
	assert.Equal(t, models.AgentRunStateCompleted, run.State)
	assert.Equal(t, input.Finish.Child.ResponseText, run.ResponseText)
	assert.NotNil(t, run.EndedAt)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store := newTestStore(t, handle)
	_, err = store.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		claim.Lease, input)
	require.NoError(t, err)
	input.Finish.Child.ResponseText = "changed child response"
	_, err = store.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestChildExecutionFinishPendingInboxRollsBackRun(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	_, err := fixture.store.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, child.ID, inboxTestInput())
	require.NoError(t, err)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, uuid.New(), finishActivityTestInput())
	require.NoError(t, err)
	input := finishActivityTestResult()
	input.Finish.Child = &ExecutionFinishChild{
		State: models.AgentRunStateCompleted, ResponseText: "child response",
	}
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.ErrorIs(t, err, ErrExecutionInboxPending)
	run, err := fixture.store.GetAgentRun(t.Context(), fixture.sessionID,
		*child.AgentRunID)
	require.NoError(t, err)
	assert.Equal(t, models.AgentRunStateRunning, run.State)
	assert.Nil(t, run.EndedAt)
	activity, err := fixture.store.GetExecutionActivity(t.Context(),
		fixture.sessionID, claim.Activity.ID)
	require.NoError(t, err)
	assert.Nil(t, activity.OutputJSON)
}

func TestChildExecutionFinishRejectsRootAndMismatchedOutcome(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, uuid.New(), finishActivityTestInput())
	require.NoError(t, err)
	input := finishActivityTestResult()
	input.Finish.Child = &ExecutionFinishChild{
		State: models.AgentRunStateCompleted,
	}
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	input.Finish.Child.State = models.AgentRunStateFailed
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}
