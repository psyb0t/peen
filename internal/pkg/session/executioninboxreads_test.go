package session

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetExecutionMessageByRequestRetainsPausedAcceptanceAfterReopen(
	t *testing.T,
) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	input := inboxTestInput()
	accepted, err := fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.NoError(t, err)
	_, err = fixture.store.PauseExecution(t.Context(), fixture.sessionID,
		fixture.root.ID, executionTestPauseReason)
	require.NoError(t, err)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store := newTestStore(t, handle)
	actual, err := store.GetExecutionMessageByRequest(t.Context(),
		fixture.sessionID, fixture.root.ID, input.RequestID)
	require.NoError(t, err)
	assert.Equal(t, accepted.Message, actual)
	execution, err := store.GetExecution(t.Context(), fixture.sessionID,
		fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, execution.State)
	otherTurn := startAgentRunTestTurn(t.Context(), t, store, fixture.sessionID)
	other, err := store.CreateExecution(t.Context(), fixture.sessionID,
		executionTestInput(otherTurn.TurnID))
	require.NoError(t, err)

	testCases := []struct {
		name                              string
		sessionID, executionID, requestID uuid.UUID
	}{
		{
			name: "wrong session", sessionID: uuid.New(),
			executionID: fixture.root.ID, requestID: input.RequestID,
		},
		{
			name: "wrong execution", sessionID: fixture.sessionID,
			executionID: uuid.New(), requestID: input.RequestID,
		},
		{
			name: "existing other execution", sessionID: fixture.sessionID,
			executionID: other.ID, requestID: input.RequestID,
		},
		{
			name: "missing request", sessionID: fixture.sessionID,
			executionID: fixture.root.ID, requestID: uuid.New(),
		},
		{
			name: "empty request", sessionID: fixture.sessionID,
			executionID: fixture.root.ID,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			row, err := store.GetExecutionMessageByRequest(t.Context(),
				tc.sessionID, tc.executionID, tc.requestID)
			require.ErrorIs(t, err, commerr.ErrNotFound)
			assert.Nil(t, row)
		})
	}
}

func TestGetExecutionMessageByRequestRespectsCancellation(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	row, err := fixture.store.GetExecutionMessageByRequest(ctx,
		fixture.sessionID, fixture.root.ID, uuid.New())
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, row)
}

func TestListExecutionMessagesWalksPausedInboxWithoutWaking(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	expected := make([]uuid.UUID, 0, 13)

	for range 13 {
		input := inboxTestInput()
		accepted, err := fixture.store.AcceptExecutionMessage(t.Context(),
			fixture.root.ID, lease, input)
		require.NoError(t, err)
		expected = append(expected, accepted.Message.ID)
	}

	_, err := fixture.store.PauseExecution(t.Context(), fixture.sessionID,
		fixture.root.ID, executionTestPauseReason)
	require.NoError(t, err)
	var actual []uuid.UUID

	for pageIndex, count := range []int{5, 5, 3, 0} {
		page, err := fixture.store.ListExecutionMessages(t.Context(),
			fixture.sessionID, fixture.root.ID, ListExecutionInboxOptions{
				Limit: 5, Offset: pageIndex * 5,
				State: models.InboxMessageStateQueued,
			})
		require.NoError(t, err)
		assert.Len(t, page.Items, count)
		assert.Equal(t, pageIndex < 2, page.HasMore)
		for _, row := range page.Items {
			actual = append(actual, row.ID)
		}
	}

	assert.Equal(t, expected, actual)
	for _, offset := range []int{13, 1000} {
		page, err := fixture.store.ListExecutionMessages(t.Context(),
			fixture.sessionID, fixture.root.ID,
			ListExecutionInboxOptions{Limit: 5, Offset: offset})
		require.NoError(t, err)
		assert.Empty(t, page.Items)
		assert.False(t, page.HasMore)
	}

	execution, err := fixture.store.GetExecution(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, execution.State)
	_, err = fixture.store.ListExecutionMessages(t.Context(), uuid.New(),
		fixture.root.ID, ListExecutionInboxOptions{})
	require.ErrorIs(t, err, commerr.ErrNotFound)
	_, err = fixture.store.ListExecutionMessages(t.Context(), fixture.sessionID,
		fixture.root.ID, ListExecutionInboxOptions{State: "invalid"})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}
