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

func inboxTestModelInput(
	t *testing.T,
	requestIDs []uuid.UUID,
) ScheduleActivityInput {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"execution": map[string]any{
			"context": map[string]any{"inboxIds": requestIDs},
		},
	})
	require.NoError(t, err)
	input := activityTestInput(models.ActivityKindModel)
	input.Input = encoded

	return input
}

func inboxTestClaim(
	t *testing.T,
	fixture executionFixture,
	requestIDs []uuid.UUID,
) *ActivityClaim {
	t.Helper()
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, uuid.New(),
		inboxTestModelInput(t, requestIDs))
	require.NoError(t, err)

	return claim
}

func TestDeliverExecutionMessagesRetriesWithoutDuplicateTranscript(
	t *testing.T,
) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	accepted := inboxTestInput()
	_, err := fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, accepted)
	require.NoError(t, err)
	requestIDs := []uuid.UUID{accepted.RequestID}
	first := inboxTestClaim(t, fixture, requestIDs)
	input := DeliverExecutionMessagesInput{
		TurnLease: lease, ModelLease: first.Lease, RequestIDs: requestIDs,
	}
	events, err := fixture.store.DeliverExecutionMessages(t.Context(),
		fixture.sessionID, input)
	require.NoError(t, err)
	require.Len(t, events.Events, 2)
	assert.Equal(t, EventTypeUserMessageDelivered, events.Events[0].EventType)
	assert.Equal(t, accepted.RequestID, events.Events[0].RequestID)
	assert.Equal(t, EventTypeSkillInjected, events.Events[1].EventType)
	repeated, err := fixture.store.DeliverExecutionMessages(t.Context(),
		fixture.sessionID, input)
	require.NoError(t, err)
	assert.Empty(t, repeated.Events)
	assert.Empty(t, repeated.Messages)
	retry := inboxTestClaim(t, fixture, requestIDs)
	assert.Equal(t, first.Activity.ID, retry.Activity.ID)
	assert.Equal(t, first.Lease.Attempt+1, retry.Lease.Attempt)
	input.ModelLease = retry.Lease
	repeated, err = fixture.store.DeliverExecutionMessages(t.Context(),
		fixture.sessionID, input)
	require.NoError(t, err)
	assert.Empty(t, repeated.Events)
	assert.Empty(t, repeated.Messages)
	messages, err := fixture.store.ListMessages(t.Context(), fixture.sessionID,
		ListMessagesOptions{})
	require.NoError(t, err)
	require.Len(t, messages.Items, 3)
	assert.Equal(t, accepted.Message, messages.Items[1].Content)
	assert.Equal(t, accepted.Parts[0].Content, messages.Items[2].Content)
	assert.True(t, messages.Items[2].Injected)
	page, err := fixture.store.ListExecutionMessages(t.Context(),
		fixture.sessionID, fixture.root.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.InboxMessageStateDelivered, page.Items[0].State)
	assert.Equal(t, first.Activity.ID, *page.Items[0].DeliveredActivityID)
}

func TestDeliverExecutionMessagesRollsBackPartialBatch(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	accepted := inboxTestInput()
	_, err := fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, accepted)
	require.NoError(t, err)
	requestIDs := []uuid.UUID{accepted.RequestID, uuid.New()}
	claim := inboxTestClaim(t, fixture, requestIDs)
	_, err = fixture.store.DeliverExecutionMessages(t.Context(),
		fixture.sessionID, DeliverExecutionMessagesInput{
			TurnLease: lease, ModelLease: claim.Lease, RequestIDs: requestIDs,
		})
	require.ErrorIs(t, err, commerr.ErrNotFound)
	page, err := fixture.store.ListExecutionMessages(t.Context(),
		fixture.sessionID, fixture.root.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.InboxMessageStateQueued, page.Items[0].State)
	assert.Nil(t, page.Items[0].DeliveredActivityID)
	messages, err := fixture.store.ListMessages(t.Context(), fixture.sessionID,
		ListMessagesOptions{})
	require.NoError(t, err)
	assert.Len(t, messages.Items, 1)
	events, err := fixture.store.ListEvents(t.Context(), fixture.sessionID,
		ListEventsOptions{EventTypes: []string{EventTypeUserMessageDelivered}})
	require.NoError(t, err)
	assert.Empty(t, events.Items)
}

func TestDeliverExecutionMessagesRetainsAcceptedSkillsAcrossReopen(
	t *testing.T,
) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	accepted := inboxTestInput()
	_, err := fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, accepted)
	require.NoError(t, err)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	fixture.store = newTestStore(t, handle)
	_, err = fixture.store.RecoverInterrupted(t.Context())
	require.NoError(t, err)
	lease, err = fixture.store.RestoreExecutionTurn(t.Context(),
		fixture.sessionID, fixture.root.ID, RestoreExecutionTurnInput{
			OwnerGenerationID: uuid.New(), PreviousLeaseID: lease.ID,
		})
	require.NoError(t, err)
	requestIDs := []uuid.UUID{accepted.RequestID}
	claim := inboxTestClaim(t, fixture, requestIDs)
	_, err = fixture.store.DeliverExecutionMessages(t.Context(),
		fixture.sessionID, DeliverExecutionMessagesInput{
			TurnLease: lease, ModelLease: claim.Lease, RequestIDs: requestIDs,
		})
	require.NoError(t, err)
	messages, err := fixture.store.ListMessages(t.Context(), fixture.sessionID,
		ListMessagesOptions{})
	require.NoError(t, err)
	require.Len(t, messages.Items, 3)
	assert.Equal(t, accepted.Parts[0].Content, messages.Items[2].Content)
}

func TestValidateInboxDeliveryIDs(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	testCases := []struct {
		name      string
		saved     []uuid.UUID
		requested []uuid.UUID
		wantErr   error
	}{
		{name: "exact", saved: []uuid.UUID{id}, requested: []uuid.UUID{id}},
		{name: "empty"},
		{name: "missing", saved: []uuid.UUID{id}, wantErr: commerr.ErrConflict},
		{
			name: "different", saved: []uuid.UUID{id},
			requested: []uuid.UUID{uuid.New()}, wantErr: commerr.ErrConflict,
		},
		{
			name: "nil", saved: []uuid.UUID{uuid.Nil},
			requested: []uuid.UUID{uuid.Nil}, wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "duplicate", saved: []uuid.UUID{id, id},
			requested: []uuid.UUID{id, id}, wantErr: commerr.ErrValidationFailed,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := inboxTestModelInput(t, tc.saved)
			activity := &models.ExecutionActivity{InputJSON: string(input.Input)}
			err := validateInboxDeliveryIDs(activity, tc.requested)
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestDeliverExecutionMessagesRefusesStaleModelAndPausedExecution(
	t *testing.T,
) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	accepted := inboxTestInput()
	_, err := fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, accepted)
	require.NoError(t, err)
	requestIDs := []uuid.UUID{accepted.RequestID}
	first := inboxTestClaim(t, fixture, requestIDs)
	second := inboxTestClaim(t, fixture, requestIDs)
	input := DeliverExecutionMessagesInput{
		TurnLease: lease, ModelLease: first.Lease, RequestIDs: requestIDs,
	}
	_, err = fixture.store.DeliverExecutionMessages(t.Context(),
		fixture.sessionID, input)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	input.ModelLease = second.Lease
	_, err = fixture.store.PauseExecution(t.Context(), fixture.sessionID,
		fixture.root.ID, executionTestPauseReason)
	require.NoError(t, err)
	_, err = fixture.store.DeliverExecutionMessages(t.Context(),
		fixture.sessionID, input)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	page, err := fixture.store.ListExecutionMessages(t.Context(),
		fixture.sessionID, fixture.root.ID, ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.InboxMessageStateQueued, page.Items[0].State)
}
