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

func sealedCompletionFixture(t *testing.T) (executionFixture, *ActivityClaim) {
	t.Helper()
	fixture := newExecutionFixture(t)
	claim := completionSealClaim(t, fixture)
	seal, err := fixture.store.ReserveExecutionCompletion(t.Context(),
		fixture.sessionID, claim.Lease)
	require.NoError(t, err)
	require.NotNil(t, seal)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, activityTestResult())
	require.NoError(t, err)

	return fixture, claim
}

func completedChildReport(
	t *testing.T,
	fixture executionFixture,
	parent *models.Execution,
) *models.ExecutionInboxMessage {
	t.Helper()

	return terminalChildReport(t, fixture, parent, models.AgentRunStateCompleted)
}

func terminalChildReport(
	t *testing.T,
	fixture executionFixture,
	parent *models.Execution,
	state models.AgentRunState,
) *models.ExecutionInboxMessage {
	t.Helper()
	child := executionChildForTest(t, fixture, parent)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, uuid.New(), finishActivityTestInput())
	require.NoError(t, err)
	input := finishActivityTestResult()
	input.Finish.Child = &ExecutionFinishChild{
		State: state, ResponseText: "completed child",
	}
	if state == models.AgentRunStateFailed {
		input.Finish.State = models.ExecutionStateFailed
		input.Finish.FailureDetail = "child provider interrupted"
		input.Finish.Child.FailureDetail = input.Finish.FailureDetail
	}

	input.Finish.ChildReport = "retained child completion"
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	page, err := fixture.store.ListExecutionMessages(t.Context(),
		fixture.sessionID, parent.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)

	return page.Items[0]
}

func TestSealedCompletionRetainsLateChildReportAfterReopen(t *testing.T) {
	t.Parallel()
	testCases := []models.AgentRunState{
		models.AgentRunStateCompleted, models.AgentRunStateFailed,
	}
	for _, tc := range testCases {
		t.Run(string(tc), func(t *testing.T) {
			t.Parallel()
			fixture, hook := sealedCompletionFixture(t)
			report := terminalChildReport(t, fixture, fixture.root, tc)
			claim, err := fixture.store.BeginExecutionActivity(t.Context(),
				fixture.sessionID, fixture.root.ID, hook.Lease.GenerationID,
				finishActivityTestInput())
			require.NoError(t, err)
			input := finishActivityTestResult()
			_, err = fixture.store.CompleteExecutionActivity(t.Context(),
				fixture.sessionID, claim.Lease, input)
			require.NoError(t, err)
			require.NoError(t, fixture.handle.Close())
			handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, handle.Close()) })
			store := newTestStore(t, handle)
			_, err = store.CompleteExecutionActivity(t.Context(), fixture.sessionID,
				claim.Lease, input)
			require.NoError(t, err)
			page, err := store.ListExecutionMessages(t.Context(), fixture.sessionID,
				fixture.root.ID, ListExecutionInboxOptions{})
			require.NoError(t, err)
			require.Len(t, page.Items, 1)
			assert.Equal(t, report, page.Items[0])
			assert.Equal(t, models.InboxMessageStateQueued, page.Items[0].State)
			execution, err := store.GetExecution(t.Context(), fixture.sessionID,
				fixture.root.ID)
			require.NoError(t, err)
			assert.Equal(t, models.ExecutionStateCompleted, execution.State)
		})
	}
}

func TestCompletionReservationRefusesEarlierChildReport(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	completedChildReport(t, fixture, fixture.root)
	hook := completionSealClaim(t, fixture)
	seal, err := fixture.store.ReserveExecutionCompletion(t.Context(),
		fixture.sessionID, hook.Lease)
	require.NoError(t, err)
	assert.Nil(t, seal)
}

func TestSealedCompletionRejectsChangedHookInput(t *testing.T) {
	t.Parallel()
	fixture, hook := sealedCompletionFixture(t)
	completedChildReport(t, fixture, fixture.root)
	repository := fixture.store.query.ExecutionActivity
	_, err := repository.WithContext(t.Context()).Where(
		repository.ID.Eq(hook.Activity.ID),
	).UpdateSimple(repository.InputJSON.Value(`{"changed":true}`))
	require.NoError(t, err)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, hook.Lease.GenerationID,
		finishActivityTestInput())
	require.NoError(t, err)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, finishActivityTestResult())
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestSealedCompletionStillRefusesLateUserInput(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name          string
		foreignReport bool
		sourceEventID uuid.UUID
	}{
		{name: "ordinary user"},
		{name: "arbitrary source", sourceEventID: uuid.New()},
		{name: "foreign parent report", foreignReport: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, hook := sealedCompletionFixture(t)
			input := inboxTestInput()
			input.SourceEventID = tc.sourceEventID
			if tc.sourceEventID != uuid.Nil {
				input.RequestID = tc.sourceEventID
			}
			if tc.foreignReport {
				parent := executionChildForTest(t, fixture, fixture.root)
				report := completedChildReport(t, fixture, parent)
				input.RequestID = report.RequestID
				input.SourceEventID = *report.SourceEventID
				// Preserve request uniqueness while testing parent ownership.
				repository := fixture.store.query.ExecutionInboxMessage
				_, err := repository.WithContext(t.Context()).Where(
					repository.ID.Eq(report.ID),
				).Delete()
				require.NoError(t, err)
			}

			holder, err := fixture.store.GetExecutionTurnLease(t.Context(),
				fixture.sessionID, fixture.root.ID)
			require.NoError(t, err)
			lease := Lease{
				ID: holder.LeaseID, SessionID: fixture.sessionID,
				TurnID: fixture.root.RootTurnID,
			}
			_, err = fixture.store.AcceptExecutionMessage(t.Context(),
				fixture.root.ID, lease, input)
			require.NoError(t, err)
			claim, err := fixture.store.BeginExecutionActivity(t.Context(),
				fixture.sessionID, fixture.root.ID, hook.Lease.GenerationID,
				finishActivityTestInput())
			require.NoError(t, err)
			_, err = fixture.store.CompleteExecutionActivity(t.Context(),
				fixture.sessionID, claim.Lease, finishActivityTestResult())
			require.ErrorIs(t, err, ErrExecutionInboxPending)
		})
	}
}

func TestSealedCompletionRequiresCommittedHook(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	hook := completionSealClaim(t, fixture)
	_, err := fixture.store.ReserveExecutionCompletion(t.Context(),
		fixture.sessionID, hook.Lease)
	require.NoError(t, err)
	_, err = completedHookInboxWatermark(t.Context(), fixture.store.query,
		fixture.root)
	require.ErrorIs(t, err, commerr.ErrConflict)
}
