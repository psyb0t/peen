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

func TestChildDeliveryCommitsTranscriptAndReusesStableIdentity(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	accepted := inboxTestInput()
	_, err := fixture.store.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, child.ID, accepted)
	require.NoError(t, err)
	ids := []uuid.UUID{accepted.RequestID}
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, uuid.New(), inboxTestModelInput(t, ids))
	require.NoError(t, err)
	delivered, err := fixture.store.DeliverChildExecutionMessages(t.Context(),
		fixture.sessionID, claim.Lease, ids)
	require.NoError(t, err)
	require.Len(t, delivered.Messages, 2)
	repeated, err := fixture.store.DeliverChildExecutionMessages(t.Context(),
		fixture.sessionID, claim.Lease, ids)
	require.NoError(t, err)
	assert.Empty(t, repeated.Messages)
	history, err := fixture.store.AgentRunHistory(t.Context(), fixture.sessionID,
		*child.AgentRunID)
	require.NoError(t, err)
	require.Len(t, history.Messages, 2)
	assert.Equal(t, accepted.Message, history.Messages[0].Content)
	assert.Equal(t, accepted.Parts[0].Content, history.Messages[1].Content)
	parent, err := fixture.store.ListMessages(t.Context(), fixture.sessionID,
		ListMessagesOptions{})
	require.NoError(t, err)
	assert.Len(t, parent.Items, 1)
}

func TestChildDeliveryRollsBackPartialBatch(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	accepted := inboxTestInput()
	_, err := fixture.store.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, child.ID, accepted)
	require.NoError(t, err)
	ids := []uuid.UUID{accepted.RequestID, uuid.New()}
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, uuid.New(), inboxTestModelInput(t, ids))
	require.NoError(t, err)
	before, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		child.ID)
	require.NoError(t, err)
	_, err = fixture.store.DeliverChildExecutionMessages(t.Context(),
		fixture.sessionID, claim.Lease, ids)
	require.ErrorIs(t, err, commerr.ErrNotFound)
	history, err := fixture.store.AgentRunHistory(t.Context(), fixture.sessionID,
		*child.AgentRunID)
	require.NoError(t, err)
	assert.Empty(t, history.Messages)
	row, err := fixture.store.GetExecutionMessageByRequest(t.Context(),
		fixture.sessionID, child.ID, accepted.RequestID)
	require.NoError(t, err)
	assert.Equal(t, models.InboxMessageStateQueued, row.State)
	assert.Nil(t, row.DeliveredActivityID)
	after, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		child.ID)
	require.NoError(t, err)
	assert.Equal(t, before.EventCount, after.EventCount)
}

func TestChildDeliveryRetryAcrossReopenKeepsOneTranscript(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	accepted := inboxTestInput()
	_, err := fixture.store.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, child.ID, accepted)
	require.NoError(t, err)
	ids := []uuid.UUID{accepted.RequestID}
	input := inboxTestModelInput(t, ids)
	first, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, uuid.New(), input)
	require.NoError(t, err)
	_, err = fixture.store.DeliverChildExecutionMessages(t.Context(),
		fixture.sessionID, first.Lease, ids)
	require.NoError(t, err)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store := newTestStore(t, handle)
	retry, err := store.BeginExecutionActivity(t.Context(), fixture.sessionID,
		child.ID, uuid.New(), input)
	require.NoError(t, err)
	assert.Equal(t, first.Activity.ID, retry.Activity.ID)
	assert.Equal(t, first.Lease.Attempt+1, retry.Lease.Attempt)
	_, err = store.DeliverChildExecutionMessages(t.Context(), fixture.sessionID,
		first.Lease, ids)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	repeated, err := store.DeliverChildExecutionMessages(t.Context(),
		fixture.sessionID, retry.Lease, ids)
	require.NoError(t, err)
	assert.Empty(t, repeated.Messages)
	history, err := store.AgentRunHistory(t.Context(), fixture.sessionID,
		*child.AgentRunID)
	require.NoError(t, err)
	assert.Len(t, history.Messages, 2)
}

func TestChildDeliveryRefusesPausedExecution(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	accepted := inboxTestInput()
	_, err := fixture.store.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, child.ID, accepted)
	require.NoError(t, err)
	ids := []uuid.UUID{accepted.RequestID}
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, uuid.New(), inboxTestModelInput(t, ids))
	require.NoError(t, err)
	_, err = fixture.store.PauseExecution(t.Context(), fixture.sessionID,
		child.ID, executionTestPauseReason)
	require.NoError(t, err)
	_, err = fixture.store.DeliverChildExecutionMessages(t.Context(),
		fixture.sessionID, claim.Lease, ids)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	history, err := fixture.store.AgentRunHistory(t.Context(), fixture.sessionID,
		*child.AgentRunID)
	require.NoError(t, err)
	assert.Empty(t, history.Messages)
}

func TestChildDeliveryRefusesSiblingRequest(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	sibling := executionChildForTest(t, fixture, fixture.root)
	accepted := inboxTestInput()
	_, err := fixture.store.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, sibling.ID, accepted)
	require.NoError(t, err)
	ids := []uuid.UUID{accepted.RequestID}
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, uuid.New(), inboxTestModelInput(t, ids))
	require.NoError(t, err)
	_, err = fixture.store.DeliverChildExecutionMessages(t.Context(),
		fixture.sessionID, claim.Lease, ids)
	require.ErrorIs(t, err, commerr.ErrConflict)
	for _, runID := range []uuid.UUID{*child.AgentRunID, *sibling.AgentRunID} {
		history, err := fixture.store.AgentRunHistory(t.Context(),
			fixture.sessionID, runID)
		require.NoError(t, err)
		assert.Empty(t, history.Messages)
	}
}

func TestChildDeliverySequenceExhaustionRollsBackTranscript(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	accepted := inboxTestInput()
	_, err := fixture.store.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, child.ID, accepted)
	require.NoError(t, err)
	ids := []uuid.UUID{accepted.RequestID}
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, uuid.New(), inboxTestModelInput(t, ids))
	require.NoError(t, err)
	repository := fixture.store.query.Execution
	_, err = repository.WithContext(t.Context()).Where(
		repository.ID.Eq(child.ID),
	).UpdateSimple(repository.EventCount.Value(math.MaxInt64))
	require.NoError(t, err)
	_, err = fixture.store.DeliverChildExecutionMessages(t.Context(),
		fixture.sessionID, claim.Lease, ids)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	history, err := fixture.store.AgentRunHistory(t.Context(), fixture.sessionID,
		*child.AgentRunID)
	require.NoError(t, err)
	assert.Empty(t, history.Messages)
	row, err := fixture.store.GetExecutionMessageByRequest(t.Context(),
		fixture.sessionID, child.ID, accepted.RequestID)
	require.NoError(t, err)
	assert.Equal(t, models.InboxMessageStateQueued, row.State)
	assert.Nil(t, row.DeliveredActivityID)
}
