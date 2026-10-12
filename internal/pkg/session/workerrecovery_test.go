package session

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoverWorkerTurnsReleasesOnlyItsLeaseAndModelCalls(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	opened := newTestSession(ctx, t, store)
	other := newTestSession(ctx, t, store)
	lease := startAgentRunTestTurn(ctx, t, store, opened.ID)
	otherLease := startAgentRunTestTurn(ctx, t, store, other.ID)
	generationID := uuid.New()
	require.NoError(t, store.RecordTurnWorkerGeneration(ctx, lease, generationID))
	require.NoError(t, store.RecordTurnWorkerGeneration(ctx, otherLease, generationID))
	modelRun, err := store.CreateModelRun(ctx, opened.ID, CreateModelRunInput{
		TurnID: lease.TurnID, Stage: models.ModelRunStageTurn,
		ModelReference: "provider/model", ConnectionName: "provider",
		RequestedModelID: "model", RequestSettingsJSON: `{}`,
	})
	require.NoError(t, err)
	_, err = store.CreateModelCall(ctx, opened.ID, modelRun.ID, CreateModelCallInput{RequestMessagesJSON: `[]`, RequestToolsJSON: `[]`})
	require.NoError(t, err)
	count, err := store.RecoverWorkerTurns(ctx, opened.ID, generationID)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assert.False(t, store.IsActive(opened.ID))
	assert.True(t, store.IsActive(other.ID))
	turns, err := store.ListTurns(ctx, opened.ID, ListTurnsOptions{})
	require.NoError(t, err)
	require.Len(t, turns.Items, 1)
	assert.Equal(t, models.TurnStateInterrupted, turns.Items[0].State)
	calls, err := store.ListModelCalls(ctx, opened.ID, modelRun.ID, ListModelCallsOptions{})
	require.NoError(t, err)
	assert.Equal(t, models.ModelRunStateInterrupted, calls.Run.State)
	require.Len(t, calls.Items, 1)
	assert.Equal(t, models.ModelCallStateInterrupted, calls.Items[0].State)
	_, err = store.AcquireTurn(ctx, opened.ID, StartTurnInput{RequestID: uuid.New(), Workspace: testWorkspace})
	require.NoError(t, err)
}
