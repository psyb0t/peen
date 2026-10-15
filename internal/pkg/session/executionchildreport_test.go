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

func TestChildReportCommitsOnceWithTerminalOutcome(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, uuid.New(), finishActivityTestInput())
	require.NoError(t, err)
	input := finishActivityTestResult()
	input.Finish.Child = &ExecutionFinishChild{
		State: models.AgentRunStateCompleted, ResponseText: "completed child",
	}
	input.Finish.ChildReport = "framed child completion"
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	page, err := fixture.store.ListExecutionMessages(t.Context(),
		fixture.sessionID, fixture.root.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, input.Finish.ChildReport, page.Items[0].Message)
	require.NotNil(t, page.Items[0].SourceEventID)
	assert.Equal(t, page.Items[0].RequestID, *page.Items[0].SourceEventID)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store := newTestStore(t, handle)
	_, err = store.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		claim.Lease, input)
	require.NoError(t, err)
	restored, err := store.ListExecutionMessages(t.Context(), fixture.sessionID,
		fixture.root.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	assert.Equal(t, page.Items, restored.Items)
	input.Finish.ChildReport = "changed report"
	_, err = store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestChildReportQueuesWithoutResumingPausedParent(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	_, err := fixture.store.PauseExecution(t.Context(), fixture.sessionID,
		fixture.root.ID, "leave parent paused")
	require.NoError(t, err)
	_, err = fixture.store.ResumeExecution(t.Context(), fixture.sessionID, child.ID)
	require.NoError(t, err)
	parent, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		fixture.root.ID)
	require.NoError(t, err)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, uuid.New(), finishActivityTestInput())
	require.NoError(t, err)
	input := finishActivityTestResult()
	input.Finish.Child = &ExecutionFinishChild{State: models.AgentRunStateCompleted}
	input.Finish.ChildReport = "completed while parent paused"
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	after, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, after.State)
	assert.Equal(t, parent.ControlRevision, after.ControlRevision)
	page, err := fixture.store.ListExecutionMessages(t.Context(),
		fixture.sessionID, fixture.root.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.InboxMessageStateQueued, page.Items[0].State)
}

func TestChildReportParentCounterFailureRollsBackChild(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	repository := fixture.store.query.Execution
	_, err := repository.WithContext(t.Context()).Where(
		repository.ID.Eq(fixture.root.ID),
	).UpdateSimple(repository.EventCount.Value(math.MaxInt64))
	require.NoError(t, err)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, uuid.New(), finishActivityTestInput())
	require.NoError(t, err)
	input := finishActivityTestResult()
	input.Finish.Child = &ExecutionFinishChild{State: models.AgentRunStateCompleted}
	input.Finish.ChildReport = "report must roll back"
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	run, err := fixture.store.GetAgentRun(t.Context(), fixture.sessionID,
		*child.AgentRunID)
	require.NoError(t, err)
	assert.Equal(t, models.AgentRunStateRunning, run.State)
	assert.Zero(t, run.EventCount)
	page, err := fixture.store.ListExecutionMessages(t.Context(),
		fixture.sessionID, fixture.root.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	activity, err := fixture.store.GetExecutionActivity(t.Context(),
		fixture.sessionID, claim.Activity.ID)
	require.NoError(t, err)
	assert.Nil(t, activity.OutputJSON)
}

func TestChildReportTargetsRootAndFullNestedInbox(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		nested    bool
		wantCount int
	}{
		{name: "root parent", wantCount: 1},
		{name: "nested full inbox", nested: true, wantCount: 2},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionFixture(t)
			parent := fixture.root
			if tc.nested {
				parent = executionChildForTest(t, fixture, parent)
				queued := inboxTestInput()
				queued.Capacity = 1 + len(queued.Parts)
				_, err := fixture.store.AcceptChildExecutionMessage(t.Context(),
					fixture.sessionID, parent.ID, queued)
				require.NoError(t, err)
				queued.RequestID = uuid.New()
				_, err = fixture.store.AcceptChildExecutionMessage(t.Context(),
					fixture.sessionID, parent.ID, queued)
				require.ErrorIs(t, err, commerr.ErrConflict)
			}
			child := executionChildForTest(t, fixture, parent)
			claim, err := fixture.store.BeginExecutionActivity(t.Context(),
				fixture.sessionID, child.ID, uuid.New(), finishActivityTestInput())
			require.NoError(t, err)
			input := finishActivityTestResult()
			input.Finish.Child = &ExecutionFinishChild{
				State: models.AgentRunStateCompleted,
			}
			input.Finish.ChildReport = "owned completion report"
			_, err = fixture.store.CompleteExecutionActivity(t.Context(),
				fixture.sessionID, claim.Lease, input)
			require.NoError(t, err)
			page, err := fixture.store.ListExecutionMessages(t.Context(),
				fixture.sessionID, parent.ID, ListExecutionInboxOptions{})
			require.NoError(t, err)
			require.Len(t, page.Items, tc.wantCount)
			assert.Equal(t, input.Finish.ChildReport,
				page.Items[len(page.Items)-1].Message)
		})
	}
}

func TestChildReportRequiresOutcome(t *testing.T) {
	t.Parallel()
	require.NoError(t, validateChildReportWithoutOutcome(""))
	require.ErrorIs(t, validateChildReportWithoutOutcome("orphan report"),
		commerr.ErrValidationFailed)
}
