package session

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func deferredForwardingFixture(
	t *testing.T,
	finishSource bool,
) (executionFixture, CreateExecutionInput) {
	t.Helper()
	fixture, hook := sealedCompletionFixture(t)
	lease := deferredTestLease(t, fixture)
	input := inboxTestInput()
	accepted, err := fixture.store.DeferExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.NoError(t, err)
	if finishSource {
		claim, err := fixture.store.BeginExecutionActivity(t.Context(),
			fixture.sessionID, fixture.root.ID, hook.Lease.GenerationID,
			finishActivityTestInput())
		require.NoError(t, err)
		_, err = fixture.store.CompleteExecutionActivity(t.Context(),
			fixture.sessionID, claim.Lease, finishActivityTestResult())
		require.NoError(t, err)
	}

	fixture.store.ReleaseTurn(lease)
	next, err := fixture.store.AcquireTurn(t.Context(), fixture.sessionID,
		StartTurnInput{
			RequestID: input.RequestID,
			Workspace: input.WorkingDirectory,
		})
	require.NoError(t, err)

	return fixture, CreateExecutionInput{
		ID: uuid.New(), RootTurnID: next.TurnID,
		DeferredInboxID: &accepted.Message.ID,
		WorkflowVersion: "exampleV1", Task: input.Message,
		WorkingDirectory: input.WorkingDirectory,
		Context:          json.RawMessage(executionTestContext),
	}
}

func TestDeferredRootCreationRetainsHandoffAfterReopenAndPause(t *testing.T) {
	t.Parallel()
	fixture, input := deferredForwardingFixture(t, true)
	target, err := fixture.store.CreateExecution(t.Context(), fixture.sessionID,
		input)
	require.NoError(t, err)
	page, err := fixture.store.ListExecutionMessages(t.Context(),
		fixture.sessionID, fixture.root.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.InboxMessageStateForwarded, page.Items[0].State)
	require.NotNil(t, page.Items[0].ForwardedExecutionID)
	assert.Equal(t, target.ID, *page.Items[0].ForwardedExecutionID)
	_, err = fixture.store.PauseExecution(t.Context(), fixture.sessionID,
		target.ID, "retain next turn")
	require.NoError(t, err)
	repository := fixture.store.query.Execution
	_, err = repository.WithContext(t.Context()).Where(
		repository.ID.Eq(target.ID),
	).UpdateSimple(repository.WorkingDirectory.Value("/later/directory"))
	require.NoError(t, err)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store := newTestStore(t, handle)
	repeated, err := store.CreateExecution(t.Context(), fixture.sessionID, input)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, repeated.State)
	assert.Equal(t, "/later/directory", repeated.WorkingDirectory)
	restored, err := store.ListExecutionMessages(t.Context(), fixture.sessionID,
		fixture.root.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	assert.Equal(t, page.Items, restored.Items)
	input.ID = uuid.New()
	_, err = store.CreateExecution(t.Context(), fixture.sessionID, input)
	require.ErrorIs(t, err, commerr.ErrAlreadyExists)
}

func TestDeferredRootCreationRefusesIdentityChangesAndRollsBack(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		change  func(*CreateExecutionInput)
		wantErr error
	}{
		{name: "text", change: func(input *CreateExecutionInput) {
			input.Task = "different task"
		}, wantErr: commerr.ErrConflict},
		{name: "directory", change: func(input *CreateExecutionInput) {
			input.WorkingDirectory = "/different"
		}, wantErr: commerr.ErrConflict},
		{name: "missing inbox", change: func(input *CreateExecutionInput) {
			id := uuid.New()
			input.DeferredInboxID = &id
		}, wantErr: commerr.ErrNotFound},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, input := deferredForwardingFixture(t, true)
			tc.change(&input)
			_, err := fixture.store.CreateExecution(t.Context(),
				fixture.sessionID, input)
			require.ErrorIs(t, err, tc.wantErr)
			_, err = fixture.store.GetExecution(t.Context(), fixture.sessionID,
				input.ID)
			require.ErrorIs(t, err, commerr.ErrNotFound)
			page, err := fixture.store.ListExecutionMessages(t.Context(),
				fixture.sessionID, fixture.root.ID, ListExecutionInboxOptions{})
			require.NoError(t, err)
			require.Len(t, page.Items, 1)
			assert.Equal(t, models.InboxMessageStateDeferred, page.Items[0].State)
			assert.Nil(t, page.Items[0].ForwardedExecutionID)
		})
	}
}

func TestDeferredRootCreationWaitsForSourceCompletion(t *testing.T) {
	t.Parallel()
	fixture, input := deferredForwardingFixture(t, false)
	_, err := fixture.store.CreateExecution(t.Context(), fixture.sessionID, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	_, err = fixture.store.GetExecution(t.Context(), fixture.sessionID, input.ID)
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

func TestDeferredRootCreationRejectsAnotherRequest(t *testing.T) {
	t.Parallel()
	fixture, input := deferredForwardingFixture(t, true)
	repository := fixture.store.query.Turn
	_, err := repository.WithContext(t.Context()).Where(
		repository.ID.Eq(input.RootTurnID),
	).UpdateSimple(repository.RequestID.Value(uuid.New()))
	require.NoError(t, err)
	_, err = fixture.store.CreateExecution(t.Context(), fixture.sessionID, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	_, err = fixture.store.GetExecution(t.Context(), fixture.sessionID, input.ID)
	require.ErrorIs(t, err, commerr.ErrNotFound)
}
