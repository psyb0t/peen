package session

import (
	"context"
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// inboxTestResumeNotice stands in for the notice the agent layer renders. The
// store keeps it as written.
const inboxTestResumeNotice = "paused by a test stop"

func TestChildInboxAcceptsAfterParentTurnCompletes(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	lease := inboxTestLease(t, fixture)
	require.NoError(t, fixture.store.FinalizeTurn(t.Context(), lease,
		FinalizeTurnInput{State: models.TurnStateCompleted}))
	input := inboxTestInput()
	accepted, err := fixture.store.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, child.ID, input)
	require.NoError(t, err)
	assert.Equal(t, child.ID, accepted.Message.ExecutionID)
	assert.Equal(t, fixture.turnID, accepted.Message.TurnID)
	repeated, err := fixture.store.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, child.ID, input)
	require.NoError(t, err)
	assert.Equal(t, accepted.Message.ID, repeated.Message.ID)
	page, err := fixture.store.ListExecutionMessages(t.Context(),
		fixture.sessionID, child.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	parent, err := fixture.store.ListExecutionMessages(t.Context(),
		fixture.sessionID, fixture.root.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	assert.Empty(t, parent.Items)
	history, err := fixture.store.ListExecutionEvents(t.Context(),
		fixture.sessionID, child.ID, ListEventsOptions{})
	require.NoError(t, err)
	require.Len(t, history.Items, 2)
	assert.Equal(t, string(ExecutionEventTypeInboxQueued),
		history.Items[1].EventType)
}

func TestChildInboxFinishedExecutionReturnsAgentFinished(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		state models.ExecutionState
	}{
		{name: "completed", state: models.ExecutionStateCompleted},
		{name: "failed", state: models.ExecutionStateFailed},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionFixture(t)
			child := executionChildForTest(t, fixture, fixture.root)
			repository := fixture.store.query.Execution
			_, err := repository.WithContext(t.Context()).Where(
				repository.ID.Eq(child.ID),
			).UpdateSimple(repository.State.Value(string(tc.state)))
			require.NoError(t, err)
			_, err = fixture.store.AcceptChildExecutionMessage(t.Context(),
				fixture.sessionID, child.ID, inboxTestInput())
			require.ErrorIs(t, err, ErrAgentRunFinished)
			assert.ErrorIs(t, err, commerr.ErrConflict)
			page, err := fixture.store.ListExecutionMessages(t.Context(),
				fixture.sessionID, child.ID, ListExecutionInboxOptions{})
			require.NoError(t, err)
			assert.Empty(t, page.Items)
		})
	}
}

func TestChildInboxReopenPreservesPauseAndRejectsChangedRepeat(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	input := inboxTestInput()
	accepted, err := fixture.store.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, child.ID, input)
	require.NoError(t, err)
	_, err = fixture.store.PauseExecution(t.Context(), fixture.sessionID,
		child.ID, executionTestPauseReason)
	require.NoError(t, err)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store := newTestStore(t, handle)
	repeated, err := store.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, child.ID, input)
	require.NoError(t, err)
	assert.Equal(t, accepted.Message.ID, repeated.Message.ID)
	paused, err := store.GetExecution(t.Context(), fixture.sessionID, child.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, paused.State)
	input.Message = "changed request"
	_, err = store.AcceptChildExecutionMessage(t.Context(), fixture.sessionID,
		child.ID, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	input.RequestID = uuid.New()
	resumed, err := store.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, child.ID, input)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{child.ID}, resumed.Resumed)
}

// A new message resumes a paused child and the paused work under it, and
// queues the resume notice ahead of the message in the same transaction.
func TestChildInboxMessageResumesThePausedSubtreeWithItsNotice(t *testing.T) {
	t.Parallel()

	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	grandchild := executionChildForTest(t, fixture, child)

	paused, err := fixture.store.PauseExecution(t.Context(), fixture.sessionID,
		child.ID, executionTestPauseReason)
	require.NoError(t, err)
	require.ElementsMatch(t, []uuid.UUID{child.ID, grandchild.ID}, paused)

	input := inboxTestInput()
	input.ResumeNotice = inboxTestResumeNotice

	accepted, err := fixture.store.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, child.ID, input)
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{child.ID, grandchild.ID},
		accepted.Resumed)
	assert.Equal(t, input.RequestID, accepted.Message.RequestID)

	for _, id := range []uuid.UUID{child.ID, grandchild.ID} {
		stored, err := fixture.store.GetExecution(t.Context(),
			fixture.sessionID, id)
		require.NoError(t, err)
		assert.NotEqual(t, models.ExecutionStatePaused, stored.State)
	}

	page, err := fixture.store.ListExecutionMessages(t.Context(),
		fixture.sessionID, child.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.Equal(t, inboxTestResumeNotice, page.Items[0].Message)
	assert.Equal(t,
		uuid.NewSHA1(input.RequestID, []byte(resumeNoticeIdentity)),
		page.Items[0].RequestID)
	assert.Equal(t, input.Message, page.Items[1].Message)

	history, err := fixture.store.ListExecutionEvents(t.Context(),
		fixture.sessionID, child.ID, ListEventsOptions{})
	require.NoError(t, err)

	kinds := make([]string, 0, len(history.Items))
	for _, event := range history.Items {
		kinds = append(kinds, event.EventType)
	}

	assert.Equal(t, []string{
		string(ExecutionEventTypeCreated),
		string(ExecutionEventTypePaused),
		string(ExecutionEventTypeResumed),
		string(ExecutionEventTypeInboxQueued),
		string(ExecutionEventTypeInboxQueued),
	}, kinds)

	repeated, err := fixture.store.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, child.ID, input)
	require.NoError(t, err)
	assert.Equal(t, accepted.Message.ID, repeated.Message.ID)
	assert.Empty(t, repeated.Resumed)
}

// A running child ignores a resume notice, because nothing paused it.
func TestChildInboxRunningChildIgnoresTheResumeNotice(t *testing.T) {
	t.Parallel()

	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)

	input := inboxTestInput()
	input.ResumeNotice = inboxTestResumeNotice

	accepted, err := fixture.store.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, child.ID, input)
	require.NoError(t, err)
	assert.Empty(t, accepted.Resumed)

	page, err := fixture.store.ListExecutionMessages(t.Context(),
		fixture.sessionID, child.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, input.Message, page.Items[0].Message)
}

// A message that does not fit leaves the paused child paused.
func TestChildInboxFullPausedChildStaysPaused(t *testing.T) {
	t.Parallel()

	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)

	_, err := fixture.store.PauseExecution(t.Context(), fixture.sessionID,
		child.ID, executionTestPauseReason)
	require.NoError(t, err)

	input := inboxTestInput()
	input.ResumeNotice = inboxTestResumeNotice
	input.Capacity = 1

	_, err = fixture.store.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, child.ID, input)
	require.ErrorIs(t, err, commerr.ErrConflict)

	stored, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		child.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, stored.State)
}

func TestChildInboxRefusesForeignSessionRootAndCapacity(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	input := inboxTestInput()
	_, err := fixture.store.AcceptChildExecutionMessage(t.Context(), uuid.New(),
		child.ID, input)
	require.ErrorIs(t, err, commerr.ErrNotFound)
	_, err = fixture.store.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, fixture.root.ID, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	input.Capacity = 1
	_, err = fixture.store.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, child.ID, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	page, err := fixture.store.ListExecutionMessages(t.Context(),
		fixture.sessionID, child.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}

func TestChildInboxSequenceFailureRollsBackAcceptance(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	repository := fixture.store.query.Execution
	_, err := repository.WithContext(t.Context()).Where(
		repository.ID.Eq(child.ID),
	).UpdateSimple(repository.EventCount.Value(math.MaxInt64))
	require.NoError(t, err)
	_, err = fixture.store.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, child.ID, inboxTestInput())
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	page, err := fixture.store.ListExecutionMessages(t.Context(),
		fixture.sessionID, child.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}

func TestChildInboxConcurrentSameRequestCommitsOnce(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	input := inboxTestInput()
	results := make(chan *ExecutionInboxAcceptance, 2)
	errors := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		go func(ctx context.Context) {
			defer func() {
				if recover() != nil {
					results <- nil
					errors <- ctxerrors.Wrap(commerr.ErrInvalidState,
						"child inbox acceptance panicked")
				}
			}()
			<-start
			result, err := fixture.store.AcceptChildExecutionMessage(ctx,
				fixture.sessionID, child.ID, input)
			results <- result
			errors <- err
		}(t.Context())
	}

	close(start)
	require.NoError(t, <-errors)
	require.NoError(t, <-errors)
	first, second := <-results, <-results
	assert.Equal(t, first.Message.ID, second.Message.ID)
}
