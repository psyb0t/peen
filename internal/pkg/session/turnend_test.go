package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const turnEndTestError = "call the model: upstream refused the request"

// startRunningTurn opens a session with one running turn holding a user
// message, the state Stop, a failed workflow, or a crash leaves behind.
func startRunningTurn(
	ctx context.Context,
	t *testing.T,
	store *Store,
) (*models.Session, Lease) {
	t.Helper()

	session := newTestSession(ctx, t, store)
	lease, err := store.AcquireTurn(ctx, session.ID, StartTurnInput{
		RequestID: uuid.New(),
		Workspace: testWorkspace,
		Messages: []MessageInput{{
			Role:    models.MessageRoleUser,
			Content: "do the work",
		}},
	})
	require.NoError(t, err)

	return session, lease
}

func turnEvents(
	ctx context.Context,
	t *testing.T,
	store *Store,
	turnID uuid.UUID,
) []*models.Event {
	t.Helper()

	event := store.query.Event
	events, err := event.WithContext(ctx).
		Where(event.TurnID.Eq(turnID)).
		Order(event.Sequence).
		Find()
	require.NoError(t, err)

	return events
}

func sessionNotices(
	ctx context.Context,
	t *testing.T,
	store *Store,
	sessionID uuid.UUID,
) []*models.SessionNotice {
	t.Helper()

	page, err := store.ListSessionNotices(ctx, sessionID,
		ListSessionNoticesOptions{})
	require.NoError(t, err)

	return page.Items
}

// requireTurnEndFrame decodes a stored turn.failed or turn.cancelled frame
// and compares it with want.
func requireTurnEndFrame(
	t *testing.T,
	frame *models.Event,
	wantType string,
	want turnEndPayload,
) {
	t.Helper()

	assert.Equal(t, wantType, frame.EventType)

	var got turnEndPayload
	require.NoError(t, json.Unmarshal([]byte(frame.PayloadJSON), &got))
	assert.Equal(t, want, got)
}

func TestEndRunningTurn(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		end           TurnEnd
		wantState     models.TurnState
		wantEvent     string
		wantFrame     turnEndPayload
		wantNotice    string
		wantSummary   string
		wantNoticeErr string
	}{
		{
			name:        "stopped",
			end:         CancelledTurnEnd(),
			wantState:   models.TurnStateCancelled,
			wantEvent:   EventTypeTurnCancelled,
			wantFrame:   turnEndPayload{Reason: TurnFailureCancelled},
			wantNotice:  events.TypeTurnCancelled,
			wantSummary: turnCancelledNoticeSummary,
		},
		{
			name:      "workflow failed",
			end:       FailedTurnEnd(turnEndTestError),
			wantState: models.TurnStateFailed,
			wantEvent: EventTypeTurnFailed,
			wantFrame: turnEndPayload{
				Reason: TurnFailureAgentRun, Error: turnEndTestError,
			},
			wantNotice:    events.TypeTurnFailed,
			wantSummary:   turnFailedNoticeSummary,
			wantNoticeErr: turnEndTestError,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			store, _ := openTestStore(t)
			session, lease := startRunningTurn(ctx, t, store)

			ended, err := store.EndRunningTurn(ctx, session.ID, lease.TurnID,
				tc.end)
			require.NoError(t, err)
			assert.True(t, ended)

			turn := store.query.Turn
			stored, err := turn.WithContext(ctx).
				Where(turn.ID.Eq(lease.TurnID)).First()
			require.NoError(t, err)
			assert.Equal(t, tc.wantState, stored.State)
			assert.Equal(t, tc.end.Classification,
				stored.FailureClassification)
			assert.NotNil(t, stored.CompletedAt)

			frames := turnEvents(ctx, t, store, lease.TurnID)
			require.NotEmpty(t, frames)
			last := frames[len(frames)-1]
			requireTurnEndFrame(t, last, tc.wantEvent, tc.wantFrame)
			assert.Positive(t, last.FeedSequence)

			notices := sessionNotices(ctx, t, store, session.ID)
			require.Len(t, notices, 1)
			requireTurnEndNotice(t, notices[0], tc.wantNotice,
				tc.wantSummary, turnEndNoticeData{
					TurnID: lease.TurnID, State: tc.wantState,
					Reason: tc.end.Classification, Error: tc.wantNoticeErr,
				})

			messages, err := store.ListMessages(ctx, session.ID,
				ListMessagesOptions{Limit: 10, Order: PageOrderAscending})
			require.NoError(t, err)
			require.Len(t, messages.Items, 1)
			assert.True(t, messages.Items[0].Incomplete)

			// A turn that already ended is left as it is.
			endedAgain, err := store.EndRunningTurn(ctx, session.ID,
				lease.TurnID, CancelledTurnEnd())
			require.NoError(t, err)
			assert.False(t, endedAgain)
			assert.Len(t, turnEvents(ctx, t, store, lease.TurnID),
				len(frames))
			assert.Len(t, sessionNotices(ctx, t, store, session.ID), 1)
		})
	}
}

func requireTurnEndNotice(
	t *testing.T,
	notice *models.SessionNotice,
	wantType, wantSummary string,
	wantData turnEndNoticeData,
) {
	t.Helper()

	assert.Equal(t, wantType, notice.Type)
	assert.Equal(t, TurnEndNoticeSource, notice.Source)
	assert.Equal(t, wantSummary, notice.Summary)
	assert.Equal(t, models.NoticeDeliveryQueue, notice.Delivery)
	assert.Equal(t, models.NoticeStatePending, notice.State)
	assert.Positive(t, notice.FeedSequence)

	want, err := json.Marshal(wantData)
	require.NoError(t, err)
	assert.JSONEq(t, string(want), notice.DataJSON)
}

// A worker cannot make a frame or notice carry an unbounded error.
func TestEndRunningTurnBoundsTheError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _ := openTestStore(t)
	session, lease := startRunningTurn(ctx, t, store)

	_, err := store.EndRunningTurn(ctx, session.ID, lease.TurnID,
		FailedTurnEnd(strings.Repeat("x", 3*turnEndDetailBytes)))
	require.NoError(t, err)

	notices := sessionNotices(ctx, t, store, session.ID)
	require.Len(t, notices, 1)

	var data turnEndNoticeData
	require.NoError(t, json.Unmarshal([]byte(notices[0].DataJSON), &data))
	assert.Len(t, data.Error, turnEndDetailBytes)
}

// A turn that completes records no notice; one that fails records its error.
func TestFinalizeTurnRecordsANoticeOnlyWhenTheTurnDidNotComplete(
	t *testing.T,
) {
	t.Parallel()

	testCases := []struct {
		name        string
		state       models.TurnState
		wantNotices int
	}{
		{name: "completed", state: models.TurnStateCompleted},
		{name: "failed", state: models.TurnStateFailed, wantNotices: 1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			store, _ := openTestStore(t)
			session, lease := startRunningTurn(ctx, t, store)

			input := FinalizeTurnInput{State: tc.state}
			if tc.state != models.TurnStateCompleted {
				input.FailureClassification = TurnFailureAgentRun
				input.FailureDetail = turnEndTestError
			}

			require.NoError(t, store.FinalizeTurn(ctx, lease, input))

			notices := sessionNotices(ctx, t, store, session.ID)
			require.Len(t, notices, tc.wantNotices)

			if tc.wantNotices == 0 {
				return
			}

			requireTurnEndNotice(t, notices[0], events.TypeTurnFailed,
				turnFailedNoticeSummary, turnEndNoticeData{
					TurnID: lease.TurnID, State: tc.state,
					Reason: TurnFailureAgentRun, Error: turnEndTestError,
				})
		})
	}
}

// A turn in another session is not found, so one session cannot end
// another's turn.
func TestEndRunningTurnStaysInItsSession(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _ := openTestStore(t)
	_, lease := startRunningTurn(ctx, t, store)
	other := newTestSession(ctx, t, store)

	_, err := store.EndRunningTurn(ctx, other.ID, lease.TurnID,
		CancelledTurnEnd())
	require.Error(t, err)
}

// A turn a dead process left running ends interrupted with a stored
// turn.failed frame and notice, so a client reading the feed sees it end and
// the next turn learns why.
func TestRecoverInterruptedStoresTheTerminalFrame(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _ := openTestStore(t)
	session, lease := startRunningTurn(ctx, t, store)

	recovered, err := store.RecoverInterrupted(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, recovered)

	turn := store.query.Turn
	stored, err := turn.WithContext(ctx).
		Where(turn.ID.Eq(lease.TurnID)).First()
	require.NoError(t, err)
	assert.Equal(t, models.TurnStateInterrupted, stored.State)

	frames := turnEvents(ctx, t, store, lease.TurnID)
	require.NotEmpty(t, frames)
	requireTurnEndFrame(t, frames[len(frames)-1], EventTypeTurnFailed,
		turnEndPayload{
			Reason: TurnFailureAgentRun, Error: turnInterruptedDetail,
		})

	notices := sessionNotices(ctx, t, store, session.ID)
	require.Len(t, notices, 1)
	requireTurnEndNotice(t, notices[0], events.TypeTurnFailed,
		turnInterruptedNoticeSummary, turnEndNoticeData{
			TurnID: lease.TurnID, State: models.TurnStateInterrupted,
			Reason: TurnFailureAgentRun, Error: turnInterruptedDetail,
		})
}
