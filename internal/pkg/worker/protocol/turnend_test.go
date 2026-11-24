package protocol

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkerTurnEndAcceptsTimeoutOverRPC(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	ended, err := remote.EndRunningTurn(t.Context(), fixture.sessionID,
		execution.RootTurnID, session.TimeoutTurnEnd())
	require.NoError(t, err)
	assert.True(t, ended)
	turn, err := remote.GetExecutionRootTurn(t.Context(), fixture.sessionID,
		execution.ID)
	require.NoError(t, err)
	assert.Equal(t, models.TurnStateCancelled, turn.State)
	assert.Equal(t, session.TurnFailureTimeout, turn.FailureClassification)
	page, err := remote.ListEvents(t.Context(), fixture.sessionID,
		session.ListEventsOptions{
			TurnID:     execution.RootTurnID,
			EventTypes: []string{session.EventTypeTurnCancelled},
		})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.JSONEq(t, `{"reason":"timeout"}`, page.Items[0].PayloadJSON)
}

func TestWorkerTurnEndRejectsForgedTimeoutOverRPC(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		end     session.TurnEnd
		foreign bool
	}{
		{name: "event", end: session.TurnEnd{
			State:          models.TurnStateCancelled,
			Classification: session.TurnFailureTimeout,
			EventType:      "arbitrary.event",
		}},
		{name: "state", end: session.TurnEnd{
			State:          models.TurnStateCompleted,
			Classification: session.TurnFailureTimeout,
			EventType:      session.EventTypeTurnCancelled,
		}},
		{name: "session", end: session.TimeoutTurnEnd(), foreign: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, remote, execution := newExecutionRPCFixture(t)
			sessionID := fixture.sessionID
			wantErr := commerr.ErrValidationFailed
			if tc.foreign {
				sessionID = uuid.New()
				wantErr = commerr.ErrPermissionDenied
			}

			_, err := remote.EndRunningTurn(t.Context(), sessionID,
				execution.RootTurnID, tc.end)
			require.ErrorIs(t, err, wantErr)
			turn, err := remote.GetExecutionRootTurn(t.Context(),
				fixture.sessionID, execution.ID)
			require.NoError(t, err)
			assert.Equal(t, models.TurnStateRunning, turn.State)
		})
	}
}
