package session

import (
	"testing"

	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTurnModelSummaryCountsAllCallsAndIsolatesTurnsAndSessions(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	owner := newTestSession(ctx, t, store)
	other := newTestSession(ctx, t, store)
	lease := startAgentRunTestTurn(ctx, t, store, owner.ID)
	t.Cleanup(func() { store.ReleaseTurn(lease) })
	for index := range 3 {
		run := createModelRunForTest(ctx, t, store, owner.ID, lease.TurnID, index)
		call := createModelCallForTest(ctx, t, store, owner.ID, run.ID, index)
		_, err := store.FinalizeModelCall(ctx, owner.ID, run.ID, call.ID, FinalizeModelCallInput{
			State:               models.ModelCallStateCompleted,
			ResponseMessageJSON: modelRunTestResponseJSON,
			ResponseUsageJSON:   modelRunTestUsageJSON, RetryAttemptsJSON: `[]`,
			PromptTokens: 12, CompletionTokens: 8, TotalTokens: 20,
			WastedPromptTokens: 2, WastedCompletionTokens: 1, WastedTotalTokens: 3,
		})
		require.NoError(t, err)
		_, err = store.FinalizeModelRun(ctx, owner.ID, run.ID, FinalizeModelRunInput{
			State: models.ModelRunStateCompleted, ResponseText: "partial answer",
			ResponseMessagesJSON: `[]`, ResponseInjectionsJSON: `[]`, ResponseUsageJSON: `{}`,
		})
		require.NoError(t, err)
	}
	summary, err := store.GetTurnModelSummary(ctx, owner.ID, lease.TurnID)
	require.NoError(t, err)
	assert.Equal(t, int64(42), summary.PromptTokens)
	assert.Equal(t, int64(27), summary.CompletionTokens)
	assert.Equal(t, int64(69), summary.TotalTokens)
	assert.Equal(t, modelRunTestReference, summary.Model)
	assert.Equal(t, "partial answer", summary.Text)
	empty, err := store.GetTurnModelSummary(ctx, other.ID, lease.TurnID)
	require.NoError(t, err)
	assert.Empty(t, empty)
	require.NoError(t, store.FinalizeTurn(ctx, lease,
		FinalizeTurnInput{State: models.TurnStateCompleted}))
	otherTurn := startAgentRunTestTurn(ctx, t, store, owner.ID)
	t.Cleanup(func() { store.ReleaseTurn(otherTurn) })
	empty, err = store.GetTurnModelSummary(ctx, owner.ID, otherTurn.TurnID)
	require.NoError(t, err)
	assert.Empty(t, empty)
}
