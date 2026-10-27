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

const (
	turnEndTestError   = "call the model: upstream refused the request"
	turnEndTestMessage = "do the work"
)

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
			Content: turnEndTestMessage,
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
		wantOutcome   string
	}{
		{
			name:        "stopped",
			end:         CancelledTurnEnd(),
			wantState:   models.TurnStateCancelled,
			wantEvent:   EventTypeTurnCancelled,
			wantFrame:   turnEndPayload{Reason: TurnFailureCancelled},
			wantNotice:  events.TypeTurnCancelled,
			wantSummary: turnCancelledNoticeSummary,
			wantOutcome: turnEndCancelledOutcome,
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
			wantOutcome:   turnEndFailedOutcome,
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
				tc.wantSummary, TurnEndNotice{
					TurnID: lease.TurnID, State: tc.wantState,
					Reason: tc.end.Classification, Error: tc.wantNoticeErr,
					Message: turnEndTestMessage,
				})

			messages, err := store.ListMessages(ctx, session.ID,
				ListMessagesOptions{Limit: 10, Order: PageOrderAscending})
			require.NoError(t, err)
			require.Len(t, messages.Items, 2)
			assert.True(t, messages.Items[0].Incomplete)

			endMessage := messages.Items[1]
			assert.Equal(t, models.MessageRoleSystem, endMessage.Role)
			assert.Equal(t, lease.TurnID, endMessage.TurnID)
			assert.True(t, endMessage.Injected)
			assert.False(t, endMessage.Incomplete)
			assert.Contains(t, endMessage.Content, tc.wantOutcome)
			assert.Contains(t, endMessage.Content, tc.wantNoticeErr)

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
	wantData TurnEndNotice,
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

	var data TurnEndNotice
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

			messages, err := store.ListMessages(ctx, session.ID,
				ListMessagesOptions{Limit: 10, Order: PageOrderAscending})
			require.NoError(t, err)
			require.Len(t, messages.Items, 1+tc.wantNotices)

			if tc.wantNotices == 0 {
				return
			}

			endMessage := messages.Items[1]
			assert.Equal(t, models.MessageRoleSystem, endMessage.Role)
			assert.False(t, endMessage.Incomplete)
			assert.Contains(t, endMessage.Content, turnEndTestError)

			requireTurnEndNotice(t, notices[0], events.TypeTurnFailed,
				turnFailedNoticeSummary, TurnEndNotice{
					TurnID: lease.TurnID, State: tc.state,
					Reason: TurnFailureAgentRun, Error: turnEndTestError,
					Message: turnEndTestMessage,
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
		turnInterruptedNoticeSummary, TurnEndNotice{
			TurnID: lease.TurnID, State: models.TurnStateInterrupted,
			Reason: TurnFailureAgentRun, Error: turnInterruptedDetail,
			Message: turnEndTestMessage,
		})
}

// A message queued for a turn that is stopped before it reaches the model
// is named in the turn's notice, so the next turn knows it was sent.
func TestTurnEndNoticeNamesUndeliveredMessages(t *testing.T) {
	t.Parallel()

	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)

	queued := inboxTestInput()
	queued.Parts = nil
	_, err := fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, queued)
	require.NoError(t, err)

	ended, err := fixture.store.EndRunningTurn(t.Context(), fixture.sessionID,
		fixture.turnID, CancelledTurnEnd())
	require.NoError(t, err)
	require.True(t, ended)

	notices := sessionNotices(t.Context(), t, fixture.store, fixture.sessionID)
	require.Len(t, notices, 1)

	var data TurnEndNotice
	require.NoError(t, json.Unmarshal([]byte(notices[0].DataJSON), &data))
	assert.Equal(t, []string{queued.Message}, data.UndeliveredMessages)
	assert.Equal(t, models.TurnStateCancelled, data.State)
}

func TestTurnEndNoticeListsTheToolCallsTheTurnMade(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	store, _ := openTestStore(t)
	session, lease := startRunningTurn(ctx, t, store)
	longArgument := strings.Repeat("a", 2*turnEndToolArgumentBytes)
	calls, err := json.Marshal([]storedToolCall{
		{
			ID: "write", Name: "writeFile",
			Arguments: json.RawMessage(`{"path":"notes.txt"}`),
		},
		{
			ID: "run", Name: "runCommand",
			Arguments: json.RawMessage(`{"command":"` + longArgument + `"}`),
		},
		{
			ID: "read", Name: "readFile",
			Arguments: json.RawMessage(`{"path":"missing.txt"}`),
		},
	})
	require.NoError(t, err)
	require.NoError(t, store.AppendCheckpoint(ctx, lease, []MessageInput{
		{Role: models.MessageRoleAssistant, ToolCallsJSON: string(calls)},
		{Role: models.MessageRoleTool, ToolCallID: "write", Content: "ok"},
		{
			Role: models.MessageRoleTool, ToolCallID: "read",
			Content: "not found", IsError: true,
		},
	}, nil))

	_, err = store.EndRunningTurn(ctx, session.ID, lease.TurnID,
		FailedTurnEnd(turnEndTestError))
	require.NoError(t, err)

	notices := sessionNotices(ctx, t, store, session.ID)
	require.Len(t, notices, 1)

	var data TurnEndNotice
	require.NoError(t, json.Unmarshal([]byte(notices[0].DataJSON), &data))
	require.Len(t, data.ToolCalls, 3)
	assert.Equal(t, TurnEndToolCall{
		Name: "writeFile", Arguments: `{"path":"notes.txt"}`,
		Outcome: turnEndToolSucceeded,
	}, data.ToolCalls[0])
	assert.Equal(t, "runCommand", data.ToolCalls[1].Name)
	assert.Equal(t, turnEndToolNoResult, data.ToolCalls[1].Outcome)
	assert.Len(t, data.ToolCalls[1].Arguments, turnEndToolArgumentBytes)
	assert.Equal(t, turnEndToolFailed, data.ToolCalls[2].Outcome)
	assert.Zero(t, data.ToolCallsOmitted)

	// The next turn's history replays the failed turn as it happened, ending
	// with the system message that names the call that never returned.
	history, err := store.CompletedHistory(ctx, session.ID)
	require.NoError(t, err)

	roles := make([]models.MessageRole, 0, len(history.Messages))
	for _, message := range history.Messages {
		roles = append(roles, message.Role)
	}

	assert.Equal(t, []models.MessageRole{
		models.MessageRoleUser,
		models.MessageRoleAssistant,
		models.MessageRoleTool,
		models.MessageRoleTool,
		models.MessageRoleSystem,
	}, roles)

	endMessage := history.Messages[len(history.Messages)-1].Content
	assert.Contains(t, endMessage, turnEndFailedOutcome)
	assert.Contains(t, endMessage, turnEndTestError)
	assert.Contains(t, endMessage, "- runCommand ")
	assert.NotContains(t, endMessage, "writeFile")
	assert.NotContains(t, endMessage, "readFile")
}

// Every carried message is cut to the notice bound, and only the first
// turnEndUndeliveredLimit undelivered messages are named.
func TestTurnEndNoticeBoundsTheMessagesItCarries(t *testing.T) {
	t.Parallel()

	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)

	for range turnEndUndeliveredLimit + 2 {
		queued := inboxTestInput()
		queued.Parts = nil
		queued.Message = strings.Repeat("q", 2*turnEndMessageBytes)
		_, err := fixture.store.AcceptExecutionMessage(t.Context(),
			fixture.root.ID, lease, queued)
		require.NoError(t, err)
	}

	_, err := fixture.store.EndRunningTurn(t.Context(), fixture.sessionID,
		fixture.turnID, FailedTurnEnd(turnEndTestError))
	require.NoError(t, err)

	notices := sessionNotices(t.Context(), t, fixture.store, fixture.sessionID)
	require.Len(t, notices, 1)

	var data TurnEndNotice
	require.NoError(t, json.Unmarshal([]byte(notices[0].DataJSON), &data))
	require.Len(t, data.UndeliveredMessages, turnEndUndeliveredLimit)

	for _, message := range data.UndeliveredMessages {
		assert.Len(t, message, turnEndMessageBytes)
	}
}
