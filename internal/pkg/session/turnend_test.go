package session

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestEndRunningTurn(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		end       TurnEnd
		wantState models.TurnState
		wantEvent string
		wantData  string
	}{
		{
			name:      "stopped",
			end:       CancelledTurnEnd(),
			wantState: models.TurnStateCancelled,
			wantEvent: EventTypeTurnCancelled,
			wantData:  `{"reason":"cancelled"}`,
		},
		{
			name:      "workflow failed",
			end:       FailedTurnEnd(),
			wantState: models.TurnStateFailed,
			wantEvent: EventTypeTurnFailed,
			wantData:  `{"reason":"agentRun"}`,
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

			events := turnEvents(ctx, t, store, lease.TurnID)
			require.NotEmpty(t, events)
			last := events[len(events)-1]
			assert.Equal(t, tc.wantEvent, last.EventType)
			assert.JSONEq(t, tc.wantData, last.PayloadJSON)
			assert.Positive(t, last.FeedSequence)

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
				len(events))
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
// turn.failed frame, so a client reading the feed sees it end.
func TestRecoverInterruptedStoresTheTerminalFrame(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _ := openTestStore(t)
	_, lease := startRunningTurn(ctx, t, store)

	recovered, err := store.RecoverInterrupted(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, recovered)

	turn := store.query.Turn
	stored, err := turn.WithContext(ctx).
		Where(turn.ID.Eq(lease.TurnID)).First()
	require.NoError(t, err)
	assert.Equal(t, models.TurnStateInterrupted, stored.State)

	events := turnEvents(ctx, t, store, lease.TurnID)
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	assert.Equal(t, EventTypeTurnFailed, last.EventType)
	assert.JSONEq(t, `{"reason":"agentRun"}`, last.PayloadJSON)
}
