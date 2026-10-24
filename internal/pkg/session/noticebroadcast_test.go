package session

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	broadcastTestError      = "call the model: upstream refused the request"
	broadcastTestNoticeType = "app.deployed"
	broadcastTestSummary    = "the app was deployed"
	broadcastTestSource     = "api"
)

func childInboxForTest(
	t *testing.T,
	fixture executionFixture,
	childID uuid.UUID,
) []*models.ExecutionInboxMessage {
	t.Helper()

	page, err := fixture.store.ListExecutionMessages(t.Context(),
		fixture.sessionID, childID, ListExecutionInboxOptions{})
	require.NoError(t, err)

	return page.Items
}

// A failed turn reaches every child that is still working, in the same
// rendering the root reads. Paused and finished children are left alone.
func TestErrorNoticeReachesEveryWorkingChild(t *testing.T) {
	t.Parallel()

	fixture := newExecutionFixture(t)

	running := executionChildForTest(t, fixture, fixture.root)
	startExecutionForRecoveryTest(t, fixture.store, fixture.sessionID,
		running.ID)

	queued := executionChildForTest(t, fixture, fixture.root)

	paused := executionChildForTest(t, fixture, fixture.root)
	_, err := fixture.store.PauseExecution(t.Context(), fixture.sessionID,
		paused.ID, executionTestPauseReason)
	require.NoError(t, err)

	finished := executionChildForTest(t, fixture, fixture.root)
	repository := fixture.store.query.Execution
	_, err = repository.WithContext(t.Context()).Where(
		repository.ID.Eq(finished.ID),
	).UpdateSimple(repository.State.Value(
		string(models.ExecutionStateCompleted)))
	require.NoError(t, err)

	ended, err := fixture.store.EndRunningTurn(t.Context(), fixture.sessionID,
		fixture.turnID, FailedTurnEnd(broadcastTestError))
	require.NoError(t, err)
	require.True(t, ended)

	notices := sessionNotices(t.Context(), t, fixture.store, fixture.sessionID)
	require.Len(t, notices, 1)
	notice := notices[0]
	require.Equal(t, events.TypeTurnFailed, notice.Type)

	want := events.RenderPrompt(events.Batch{
		Notices: []events.Notice{noticeFromRecord(notice)},
	})

	for _, child := range []*models.Execution{running, queued} {
		inbox := childInboxForTest(t, fixture, child.ID)
		require.Len(t, inbox, 1, "child %s", child.ID)
		assert.Equal(t, want, inbox[0].Message)
		assert.Contains(t, inbox[0].Message, broadcastTestError)
		assert.Equal(t, uuid.NewSHA1(notice.ID, child.ID[:]),
			inbox[0].RequestID)
		require.NotNil(t, inbox[0].SourceEventID)
		assert.Equal(t, notice.ID, *inbox[0].SourceEventID)
		assert.Equal(t, models.InboxMessageStateQueued, inbox[0].State)
	}

	for _, child := range []*models.Execution{paused, finished} {
		assert.Empty(t, childInboxForTest(t, fixture, child.ID),
			"child %s", child.ID)
	}

	stored, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		paused.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, stored.State)
}

// A notice that reports no error stays with the root conversation.
func TestOrdinaryNoticeDoesNotReachChildren(t *testing.T) {
	t.Parallel()

	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)

	_, err := fixture.store.CreateSessionNotice(t.Context(), fixture.sessionID,
		CreateSessionNoticeInput{
			Type: broadcastTestNoticeType, Source: broadcastTestSource,
			Summary:  broadcastTestSummary,
			Delivery: models.NoticeDeliveryQueue,
		})
	require.NoError(t, err)

	assert.Empty(t, childInboxForTest(t, fixture, child.ID))
}

// A child's copy of an error notice gets past a full inbox, like a child's
// report to its parent does.
func TestErrorNoticeReachesAChildWithAFullInbox(t *testing.T) {
	t.Parallel()

	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)

	input := inboxTestInput()
	input.Parts = nil
	input.Capacity = 1
	_, err := fixture.store.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, child.ID, input)
	require.NoError(t, err)

	_, err = fixture.store.EndRunningTurn(t.Context(), fixture.sessionID,
		fixture.turnID, FailedTurnEnd(broadcastTestError))
	require.NoError(t, err)

	assert.Len(t, childInboxForTest(t, fixture, child.ID), 2)
}
