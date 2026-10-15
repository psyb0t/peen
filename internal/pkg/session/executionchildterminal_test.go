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

func TestChildExecutionTerminalEventCommitsOnce(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, uuid.New(), finishActivityTestInput())
	require.NoError(t, err)
	input := finishActivityTestResult()
	input.Finish.Child = &ExecutionFinishChild{
		State: models.AgentRunStateCompleted, ResponseText: "child response",
	}
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	page, err := fixture.store.ListAgentRunEvents(t.Context(), fixture.sessionID,
		*child.AgentRunID, ListAgentRunEventsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, EventTypeAgentRunCompleted, page.Items[0].EventType)
	assert.Contains(t, page.Items[0].PayloadJSON, child.AgentRunID.String())
	first := page.Items[0]
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store := newTestStore(t, handle)
	_, err = store.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		claim.Lease, input)
	require.NoError(t, err)
	page, err = store.ListAgentRunEvents(t.Context(), fixture.sessionID,
		*child.AgentRunID, ListAgentRunEventsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, first, page.Items[0])
}

func TestChildTerminalEventOverflowRollsBackFinalization(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	repository := fixture.store.query.AgentRun
	_, err := repository.WithContext(t.Context()).Where(
		repository.ID.Eq(*child.AgentRunID),
	).UpdateSimple(repository.EventCount.Value(math.MaxInt64))
	require.NoError(t, err)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, uuid.New(), finishActivityTestInput())
	require.NoError(t, err)
	input := finishActivityTestResult()
	input.Finish.Child = &ExecutionFinishChild{State: models.AgentRunStateCompleted}
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
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

func TestChildTerminalFailureEventRetainsReason(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, uuid.New(), finishActivityTestInput())
	require.NoError(t, err)
	input := finishActivityTestResult()
	input.Finish.State = models.ExecutionStateFailed
	input.Finish.FailureDetail = "child budget exhausted"
	input.Finish.Child = &ExecutionFinishChild{
		State:         models.AgentRunStateFailed,
		FailureDetail: input.Finish.FailureDetail,
	}
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	page, err := fixture.store.ListAgentRunEvents(t.Context(), fixture.sessionID,
		*child.AgentRunID, ListAgentRunEventsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, EventTypeAgentRunFailed, page.Items[0].EventType)
	assert.Contains(t, page.Items[0].PayloadJSON, input.Finish.FailureDetail)
}
