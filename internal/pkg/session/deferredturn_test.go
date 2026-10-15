package session

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeferredTurnAdmissionOpeningBatchSizes(t *testing.T) {
	t.Parallel()
	for _, count := range []int{0, 1, DefaultPageLimit, DefaultPageLimit + 3} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			t.Parallel()
			fixture, ids := deferredReadFixture(t, 1)
			fixture.store.ReleaseTurn(deferredTestLease(t, fixture))
			page, err := fixture.store.ListDeferredExecutionMessages(t.Context(),
				fixture.sessionID, ListEventsOptions{})
			require.NoError(t, err)
			require.Len(t, page.Items, 1)
			message := page.Items[0]
			input := StartTurnInput{
				RequestID: message.RequestID, Workspace: message.WorkingDirectory,
			}
			for range count {
				notice, err := fixture.store.CreateSessionNotice(t.Context(),
					fixture.sessionID, CreateSessionNoticeInput{
						Type: "app.ready", Source: "example", Summary: "ready",
						DataJSON: `{}`, Delivery: models.NoticeDeliveryQueue,
					})
				require.NoError(t, err)
				input.NoticeIDs = append(input.NoticeIDs, notice.ID)
			}
			first, err := fixture.store.AcquireDeferredExecutionTurn(t.Context(),
				fixture.sessionID, ids[0], input)
			require.NoError(t, err)
			require.Len(t, first.Notices, count)
			for index, notice := range first.Notices {
				assert.Equal(t, input.NoticeIDs[index], notice.ID)
			}
			fixture.store.ReleaseTurn(first.Lease)
			input.NoticeIDs = nil
			restored, err := fixture.store.AcquireDeferredExecutionTurn(t.Context(),
				fixture.sessionID, ids[0], input)
			require.NoError(t, err)
			assert.Equal(t, first.Notices, restored.Notices)
			fixture.store.ReleaseTurn(restored.Lease)
		})
	}
}

func TestDeferredTurnAdmissionRetainsOpeningNoticesAfterReopen(t *testing.T) {
	t.Parallel()
	fixture, ids := deferredReadFixture(t, 1)
	fixture.store.ReleaseTurn(deferredTestLease(t, fixture))
	page, err := fixture.store.ListDeferredExecutionMessages(t.Context(),
		fixture.sessionID, ListEventsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	message := page.Items[0]
	create := func() *models.SessionNotice {
		t.Helper()
		notice, err := fixture.store.CreateSessionNotice(t.Context(),
			fixture.sessionID, CreateSessionNoticeInput{
				Type: "app.ready", Source: "example", Summary: "ready",
				DataJSON: `{}`, Delivery: models.NoticeDeliveryQueue,
			})
		require.NoError(t, err)

		return notice
	}
	firstNotice, secondNotice := create(), create()
	input := StartTurnInput{
		RequestID: message.RequestID, Workspace: message.WorkingDirectory,
		NoticeIDs: []uuid.UUID{secondNotice.ID, firstNotice.ID},
		Messages: []MessageInput{{
			Role: models.MessageRoleUser, Content: message.Message,
		}},
	}
	first, err := fixture.store.AcquireDeferredExecutionTurn(t.Context(),
		fixture.sessionID, ids[0], input)
	require.NoError(t, err)
	require.Len(t, first.Notices, 2)
	assert.Equal(t, firstNotice.ID, first.Notices[0].ID)
	assert.Equal(t, secondNotice.ID, first.Notices[1].ID)
	for _, notice := range first.Notices {
		assert.Equal(t, models.NoticeStateDelivered, notice.State)
		assert.NotNil(t, notice.DeliveredAt)
	}
	newer := create()
	input.NoticeIDs = []uuid.UUID{newer.ID}
	fixture.store.ReleaseTurn(first.Lease)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store := newTestStore(t, handle)
	for range 2 {
		restored, err := store.AcquireDeferredExecutionTurn(t.Context(),
			fixture.sessionID, message.ID, input)
		require.NoError(t, err)
		assert.False(t, restored.Created)
		assert.Equal(t, first.Lease.TurnID, restored.Lease.TurnID)
		assert.Equal(t, first.Notices, restored.Notices)
		store.ReleaseTurn(restored.Lease)
	}
	live, err := store.AcquireDeferredExecutionTurn(t.Context(),
		fixture.sessionID, message.ID, input)
	require.NoError(t, err)
	cancelled, err := store.Cancel(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	assert.True(t, cancelled)
	store.ReleaseTurn(live.Lease)
	_, err = store.AcquireDeferredExecutionTurn(t.Context(), fixture.sessionID,
		message.ID, input)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	assert.False(t, store.IsActive(fixture.sessionID))
	pending, err := store.ListSessionNotices(t.Context(), fixture.sessionID,
		ListSessionNoticesOptions{State: models.NoticeStatePending})
	require.NoError(t, err)
	require.Len(t, pending.Items, 1)
	assert.Equal(t, newer.ID, pending.Items[0].ID)
	links := store.query.TurnOpeningNotice
	rows, err := links.WithContext(t.Context()).Where(
		links.SessionID.Eq(fixture.sessionID),
		links.TurnID.Eq(first.Lease.TurnID),
	).Order(links.SourceSequence).Find()
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, []uuid.UUID{firstNotice.ID, secondNotice.ID},
		[]uuid.UUID{rows[0].NoticeID, rows[1].NoticeID})
}

func TestDeferredTurnAdmissionSurvivesReopenWithoutDuplicateTranscript(
	t *testing.T,
) {
	t.Parallel()
	fixture, _ := deferredReadFixture(t, 1)
	fixture.store.ReleaseTurn(deferredTestLease(t, fixture))
	page, err := fixture.store.ListDeferredExecutionMessages(t.Context(),
		fixture.sessionID, ListEventsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	message := page.Items[0]
	input := StartTurnInput{
		RequestID: message.RequestID, Workspace: message.WorkingDirectory,
		Messages: []MessageInput{{
			Role: models.MessageRoleUser, Content: message.Message,
		}},
		Events: []EventInput{{
			EventType:   EventTypeUserMessageDelivered,
			PayloadJSON: `{}`,
		}},
	}
	first, err := fixture.store.AcquireDeferredExecutionTurn(t.Context(),
		fixture.sessionID, message.ID, input)
	require.NoError(t, err)
	require.Len(t, first.Events, 1)
	assert.True(t, first.Created)
	repeated, err := fixture.store.AcquireDeferredExecutionTurn(t.Context(),
		fixture.sessionID, message.ID, input)
	require.NoError(t, err)
	assert.Equal(t, first.Lease, repeated.Lease)
	assert.Empty(t, repeated.Events)
	assert.False(t, repeated.Created)
	fixture.store.ReleaseTurn(first.Lease)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store := newTestStore(t, handle)
	restored, err := store.AcquireDeferredExecutionTurn(t.Context(),
		fixture.sessionID, message.ID, input)
	require.NoError(t, err)
	assert.Equal(t, first.Lease.TurnID, restored.Lease.TurnID)
	assert.NotEqual(t, first.Lease.ID, restored.Lease.ID)
	assert.Empty(t, restored.Events)
	assert.False(t, restored.Created)
	repository := store.query.Message
	messages, err := repository.WithContext(t.Context()).Where(
		repository.TurnID.Eq(restored.Lease.TurnID),
	).Find()
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, message.Message, messages[0].Content)
	_, err = store.Cancel(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	store.ReleaseTurn(restored.Lease)
	_, err = store.AcquireDeferredExecutionTurn(t.Context(),
		fixture.sessionID, message.ID, input)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	assert.False(t, store.IsActive(fixture.sessionID))
}

func TestDeferredTurnAdmissionKeepsUnrelatedTurnActive(t *testing.T) {
	t.Parallel()
	fixture, ids := deferredReadFixture(t, 1)
	_, err := fixture.store.AcquireDeferredExecutionTurn(t.Context(),
		fixture.sessionID, ids[0], StartTurnInput{})
	require.ErrorIs(t, err, ErrSessionBusy)
	assert.True(t, fixture.store.IsActive(fixture.sessionID))
	_, err = fixture.store.DeferExecutionMessage(t.Context(), fixture.root.ID,
		deferredTestLease(t, fixture), inboxTestInput())
	require.ErrorIs(t, err, commerr.ErrInvalidState)
}

func TestDeferredTurnAdmissionRefusesChangedIdentity(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		change  func(*uuid.UUID, *StartTurnInput)
		wantErr error
	}{
		{name: "missing input", change: func(id *uuid.UUID,
			_ *StartTurnInput,
		) {
			*id = uuid.New()
		}, wantErr: commerr.ErrNotFound},
		{name: "request", change: func(_ *uuid.UUID, input *StartTurnInput) {
			input.RequestID = uuid.New()
		}, wantErr: commerr.ErrConflict},
		{name: "directory", change: func(_ *uuid.UUID, input *StartTurnInput) {
			input.Workspace = "/different"
		}, wantErr: commerr.ErrConflict},
		{name: "worker", change: func(_ *uuid.UUID, input *StartTurnInput) {
			input.WorkerGenerationID = "invalid"
		}, wantErr: commerr.ErrValidationFailed},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, ids := deferredReadFixture(t, 1)
			fixture.store.ReleaseTurn(deferredTestLease(t, fixture))
			page, err := fixture.store.ListDeferredExecutionMessages(t.Context(),
				fixture.sessionID, ListEventsOptions{})
			require.NoError(t, err)
			input := StartTurnInput{
				RequestID: page.Items[0].RequestID,
				Workspace: page.Items[0].WorkingDirectory,
			}
			inboxID := ids[0]
			tc.change(&inboxID, &input)
			_, err = fixture.store.AcquireDeferredExecutionTurn(t.Context(),
				fixture.sessionID, inboxID, input)
			require.ErrorIs(t, err, tc.wantErr)
			assert.False(t, fixture.store.IsActive(fixture.sessionID))
		})
	}
}

func TestDeferredTurnWakeSourceCancellation(t *testing.T) {
	t.Parallel()
	fixture, ids := deferredReadFixture(t, 1)
	fixture.store.ReleaseTurn(deferredTestLease(t, fixture))
	page, err := fixture.store.ListDeferredExecutionMessages(t.Context(),
		fixture.sessionID, ListEventsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	run := createAgentRunForTest(t.Context(), t, fixture.store,
		fixture.sessionID, fixture.turnID, 1)
	data, err := json.Marshal(events.AgentNoticeData{RunID: run.ID})
	require.NoError(t, err)
	notice, err := fixture.store.CreateSessionNotice(t.Context(), fixture.sessionID,
		CreateSessionNoticeInput{
			Type:   events.TypeAgentQuestion,
			Source: events.AgentSource, Summary: "child needs input",
			DataJSON: string(data), Delivery: models.NoticeDeliveryWake,
		})
	require.NoError(t, err)
	input := StartTurnInput{
		RequestID: page.Items[0].RequestID,
		Workspace: page.Items[0].WorkingDirectory, NoticeIDs: []uuid.UUID{notice.ID},
		WakeNoticeID: &notice.ID,
	}
	admitted, err := fixture.store.AcquireDeferredExecutionTurn(t.Context(),
		fixture.sessionID, ids[0], input)
	require.NoError(t, err)
	fixture.store.ReleaseTurn(admitted.Lease)
	_, _, err = fixture.store.RequestAgentRunCancellation(t.Context(),
		fixture.sessionID, run.ID)
	require.NoError(t, err)
	input.NoticeIDs = nil
	_, err = fixture.store.AcquireDeferredExecutionTurn(t.Context(),
		fixture.sessionID, ids[0], input)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	assert.False(t, fixture.store.IsActive(fixture.sessionID))
	notices, err := fixture.store.ListSessionNotices(t.Context(), fixture.sessionID,
		ListSessionNoticesOptions{})
	require.NoError(t, err)
	require.Len(t, notices.Items, 1)
	assert.Equal(t, models.NoticeStateDelivered, notices.Items[0].State)
}

func TestDeferredTurnRefusesMissingWakeSnapshot(t *testing.T) {
	t.Parallel()
	fixture, ids := deferredReadFixture(t, 1)
	fixture.store.ReleaseTurn(deferredTestLease(t, fixture))
	page, err := fixture.store.ListDeferredExecutionMessages(t.Context(),
		fixture.sessionID, ListEventsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	wakeID := uuid.New()
	input := StartTurnInput{
		RequestID: page.Items[0].RequestID,
		Workspace: page.Items[0].WorkingDirectory, WakeNoticeID: &wakeID,
	}
	_, err = fixture.store.AcquireDeferredExecutionTurn(t.Context(),
		fixture.sessionID, ids[0], input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	assert.False(t, fixture.store.IsActive(fixture.sessionID))
	input.WakeNoticeID = nil
	admitted, err := fixture.store.AcquireDeferredExecutionTurn(t.Context(),
		fixture.sessionID, ids[0], input)
	require.NoError(t, err)
	fixture.store.ReleaseTurn(admitted.Lease)
	assert.True(t, admitted.Created)
}
