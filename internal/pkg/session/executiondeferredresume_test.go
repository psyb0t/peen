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

func deferredResumeFixture(t *testing.T, admitted bool) (executionFixture, *models.ExecutionInboxMessage, *ActivityClaim, *DeferredTurnAdmission) {
	t.Helper()
	fixture, _ := deferredReadFixture(t, 2)
	fixture.store.ReleaseTurn(deferredTestLease(t, fixture))
	page, err := fixture.store.ListDeferredExecutionMessages(t.Context(), fixture.sessionID, ListEventsOptions{})
	require.NoError(t, err)
	message := page.Items[0]
	var previous *DeferredTurnAdmission
	if admitted {
		previous, err = fixture.store.AcquireDeferredExecutionTurn(t.Context(), fixture.sessionID, message.ID,
			StartTurnInput{RequestID: message.RequestID, Workspace: message.WorkingDirectory})
		require.NoError(t, err)
	}
	_, err = fixture.store.Cancel(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	if previous != nil {
		fixture.store.ReleaseTurn(previous.Lease)
	}
	lease := startAgentRunTestTurn(t.Context(), t, fixture.store, fixture.sessionID)
	caller, err := fixture.store.CreateExecution(t.Context(), fixture.sessionID, executionTestInput(lease.TurnID))
	require.NoError(t, err)
	callerFixture := fixture
	callerFixture.root = caller
	claim := beginActivityForTest(t, callerFixture, uuid.New(), models.ActivityKindTool)

	return fixture, message, claim, previous
}

func TestDeferredResumeRetainsSelectedInputAndAdmissionAcrossReopen(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		admitted bool
	}{
		{name: "not admitted"}, {name: "cancelled admission", admitted: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, message, claim, previous := deferredResumeFixture(t, tc.admitted)
			require.NoError(t, fixture.handle.Close())
			handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, handle.Close()) })
			store := newTestStore(t, handle)
			input := ResumeExecutionInput{SourceLease: claim.Lease, DeferredRequestID: &message.RequestID, DeferredStopRevision: &message.StopRevision}
			changed, err := store.ResumeExecutionFromActivity(t.Context(), fixture.sessionID, fixture.root.ID, input)
			require.NoError(t, err)
			assert.Empty(t, changed)
			retained, err := store.GetExecutionMessageByRequest(t.Context(), fixture.sessionID, fixture.root.ID, message.RequestID)
			require.NoError(t, err)
			assert.EqualValues(t, 1, retained.StopRevision)
			assert.Equal(t, models.InboxMessageStateDeferred, retained.State)
			assert.Equal(t, message.PartsJSON, retained.PartsJSON)
			source, err := store.GetExecution(t.Context(), fixture.sessionID, fixture.root.ID)
			require.NoError(t, err)
			assert.Equal(t, models.ExecutionStateCompleted, source.State)
			events, err := store.ListExecutionEvents(t.Context(), fixture.sessionID, fixture.root.ID, ListEventsOptions{})
			require.NoError(t, err)
			event := events.Items[len(events.Items)-1]
			assert.Equal(t, string(ExecutionEventTypeInboxResumed), event.EventType)
			var origin struct {
				SourceLease  ActivityLease `json:"sourceLease"`
				InboxID      uuid.UUID     `json:"inboxId"`
				StopRevision int64         `json:"stopRevision"`
			}
			require.NoError(t, json.Unmarshal([]byte(event.PayloadJSON), &origin))
			assert.Equal(t, claim.Lease, origin.SourceLease)
			assert.Equal(t, message.ID, origin.InboxID)
			assert.EqualValues(t, 1, origin.StopRevision)
			_, err = store.ResumeExecutionFromActivity(t.Context(), fixture.sessionID, fixture.root.ID, input)
			require.ErrorIs(t, err, commerr.ErrConflict)
			page, err := store.ListDeferredExecutionMessages(t.Context(), fixture.sessionID, ListEventsOptions{})
			require.NoError(t, err)
			require.Len(t, page.Items, 2)
			assert.Zero(t, page.Items[1].StopRevision)
			admitted, err := store.AcquireDeferredExecutionTurn(t.Context(), fixture.sessionID, message.ID,
				StartTurnInput{RequestID: message.RequestID, Workspace: message.WorkingDirectory})
			require.NoError(t, err)
			if previous != nil {
				assert.Equal(t, previous.Lease.TurnID, admitted.Lease.TurnID)
				assert.False(t, admitted.Created)
			}
			store.ReleaseTurn(admitted.Lease)
		})
	}
}

func TestDeferredResumeRefusesChangedSelectionOrCaller(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		change  func(*testing.T, executionFixture, *models.ExecutionInboxMessage, *ResumeExecutionInput)
		wantErr error
	}{
		{name: "missing inspected revision", wantErr: commerr.ErrValidationFailed, change: func(_ *testing.T, _ executionFixture, _ *models.ExecutionInboxMessage, input *ResumeExecutionInput) {
			input.DeferredStopRevision = nil
		}},
		{name: "missing selected request", wantErr: commerr.ErrValidationFailed, change: func(_ *testing.T, _ executionFixture, _ *models.ExecutionInboxMessage, input *ResumeExecutionInput) {
			input.DeferredRequestID = nil
		}},
		{name: "unknown request", wantErr: commerr.ErrNotFound, change: func(_ *testing.T, _ executionFixture, _ *models.ExecutionInboxMessage, input *ResumeExecutionInput) {
			id := uuid.New()
			input.DeferredRequestID = &id
		}},
		{name: "changed revision", wantErr: commerr.ErrConflict, change: func(_ *testing.T, _ executionFixture, _ *models.ExecutionInboxMessage, input *ResumeExecutionInput) {
			revision := int64(9)
			input.DeferredStopRevision = &revision
		}},
		{name: "later Stop", wantErr: commerr.ErrConflict, change: func(t *testing.T, f executionFixture, _ *models.ExecutionInboxMessage, _ *ResumeExecutionInput) {
			t.Helper()
			_, err := f.store.Cancel(t.Context(), f.sessionID)
			require.NoError(t, err)
		}},
		{name: "queued message", wantErr: commerr.ErrConflict, change: func(t *testing.T, f executionFixture, message *models.ExecutionInboxMessage, _ *ResumeExecutionInput) {
			t.Helper()
			repo := f.store.query.ExecutionInboxMessage
			_, err := repo.WithContext(t.Context()).Where(repo.ID.Eq(message.ID)).UpdateSimple(repo.State.Value(string(models.InboxMessageStateQueued)))
			require.NoError(t, err)
		}},
		{name: "foreign generation", wantErr: commerr.ErrPermissionDenied, change: func(_ *testing.T, _ executionFixture, _ *models.ExecutionInboxMessage, input *ResumeExecutionInput) {
			input.SourceLease.GenerationID = uuid.New()
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, message, claim, _ := deferredResumeFixture(t, false)
			input := ResumeExecutionInput{SourceLease: claim.Lease, DeferredRequestID: &message.RequestID, DeferredStopRevision: &message.StopRevision}
			tc.change(t, fixture, message, &input)
			_, err := fixture.store.ResumeExecutionFromActivity(t.Context(), fixture.sessionID, fixture.root.ID, input)
			require.ErrorIs(t, err, tc.wantErr)
			retained, err := fixture.store.GetExecutionMessageByRequest(t.Context(), fixture.sessionID, fixture.root.ID, message.RequestID)
			require.NoError(t, err)
			assert.Zero(t, retained.StopRevision)
		})
	}
}
