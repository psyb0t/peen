package session

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	agentRunTestTask         = "inspect the workspace"
	agentRunTestInstructions = "use the available tools"
	agentRunTestPrompt       = "system prompt"
	agentRunTestModel        = "provider/model"
)

func TestStoreAgentRunReplaySurvivesRestartAndPagesEveryRow(t *testing.T) {
	ctx := context.Background()
	stateDirectory := filepath.Join(t.TempDir(), "state")
	handle, err := db.Open(ctx, db.Config{Directory: stateDirectory})
	require.NoError(t, err)

	store := newTestStore(t, handle)
	session := newTestSession(ctx, t, store)
	lease := startAgentRunTestTurn(ctx, t, store, session.ID)

	runs := make([]*models.AgentRun, 0, 13)
	for index := range 13 {
		run := createAgentRunForTest(ctx, t, store, session.ID, lease.TurnID, index)
		runs = append(runs, run)
	}

	for index := range 5 {
		stored, appendErr := store.AppendAgentRunEvent(
			ctx,
			session.ID,
			runs[0].ID,
			AgentRunEventInput{
				EventType:   "agent.run.text.delta",
				PayloadJSON: fmt.Sprintf(`{"text":"delta-%d"}`, index),
			},
		)
		require.NoError(t, appendErr)
		assert.Equal(t, int64(index+1), stored.Sequence)
	}

	_, err = store.FinalizeAgentRun(ctx, session.ID, runs[0].ID, FinalizeAgentRunInput{
		State:                 models.AgentRunStateCompleted,
		ResponseText:          "complete",
		ResponseThinking:      "reasoned",
		ResponseMessagesJSON:  `[]`,
		FinishReason:          "stop",
		PromptTokenCount:      11,
		CompletionTokenCount:  7,
		FailureClassification: "",
		FailureDetail:         "",
	})
	require.NoError(t, err)

	var listed []uuid.UUID
	for offset := 0; ; offset += 5 {
		page, pageErr := store.ListAgentRuns(ctx, session.ID, ListAgentRunsOptions{
			Limit:  5,
			Offset: offset,
		})
		require.NoError(t, pageErr)
		for _, item := range page.Items {
			listed = append(listed, item.ID)
		}
		if !page.HasMore {
			break
		}
	}
	require.Len(t, listed, len(runs))
	assert.ElementsMatch(t, agentRunIDs(runs), listed)

	firstEvents, err := store.ListAgentRunEvents(
		ctx,
		session.ID,
		runs[0].ID,
		ListAgentRunEventsOptions{Limit: 2},
	)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2}, agentRunEventSequences(firstEvents.Items))
	assert.Equal(t, int64(2), firstEvents.NextCursor)
	assert.True(t, firstEvents.HasMore)
	assert.Equal(t, models.AgentRunStateCompleted, firstEvents.Run.State)
	assert.Equal(t, "complete", firstEvents.Run.ResponseText)

	require.NoError(t, handle.Close())
	reopenedHandle, err := db.Open(ctx, db.Config{Directory: stateDirectory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopenedHandle.Close()) })
	reopenedStore := newTestStore(t, reopenedHandle)

	replayed, err := reopenedStore.ListAgentRunEvents(
		ctx,
		session.ID,
		runs[0].ID,
		ListAgentRunEventsOptions{Cursor: 2, Limit: 5},
	)
	require.NoError(t, err)
	assert.Equal(t, []int64{3, 4, 5}, agentRunEventSequences(replayed.Items))
	assert.Equal(t, int64(5), replayed.NextCursor)
	assert.False(t, replayed.HasMore)

	recovered, err := reopenedStore.RecoverInterruptedAgentRuns(ctx)
	require.NoError(t, err)
	assert.Equal(t, len(runs)-1, recovered)

	interrupted, err := reopenedStore.GetAgentRun(ctx, session.ID, runs[1].ID)
	require.NoError(t, err)
	assert.Equal(t, models.AgentRunStateInterrupted, interrupted.State)
	assert.Contains(t, interrupted.FailureDetail, "process stopped")

	completed, err := reopenedStore.GetAgentRun(ctx, session.ID, runs[0].ID)
	require.NoError(t, err)
	assert.Equal(t, models.AgentRunStateCompleted, completed.State)

	secondRecovery, err := reopenedStore.RecoverInterruptedAgentRuns(ctx)
	require.NoError(t, err)
	assert.Zero(t, secondRecovery)
}

func TestStoreAgentRunCancellationAndSessionIsolation(t *testing.T) {
	ctx := context.Background()
	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	firstSession := newTestSession(ctx, t, store)
	secondSession := newTestSession(ctx, t, store)
	lease := startAgentRunTestTurn(ctx, t, store, firstSession.ID)
	run := createAgentRunForTest(ctx, t, store, firstSession.ID, lease.TurnID, 0)

	requested, didRequest, err := store.RequestAgentRunCancellation(
		ctx,
		firstSession.ID,
		run.ID,
	)
	require.NoError(t, err)
	assert.True(t, didRequest)
	assert.True(t, requested.CancelRequested)
	assert.Equal(t, models.AgentRunStateRunning, requested.State)

	again, didRequestAgain, err := store.RequestAgentRunCancellation(
		ctx,
		firstSession.ID,
		run.ID,
	)
	require.NoError(t, err)
	assert.False(t, didRequestAgain)
	assert.True(t, again.CancelRequested)

	_, err = store.GetAgentRun(ctx, secondSession.ID, run.ID)
	require.ErrorIs(t, err, commerr.ErrNotFound)

	_, err = store.ListAgentRunEvents(
		ctx,
		secondSession.ID,
		run.ID,
		ListAgentRunEventsOptions{Limit: 1},
	)
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

func startAgentRunTestTurn(
	ctx context.Context,
	t *testing.T,
	store *Store,
	sessionID uuid.UUID,
) Lease {
	t.Helper()

	lease, err := store.AcquireTurn(ctx, sessionID, StartTurnInput{
		RequestID: uuid.New(),
		Workspace: testWorkspace,
		Messages: []MessageInput{{
			Role:    models.MessageRoleUser,
			Content: "start child work",
		}},
	})
	require.NoError(t, err)

	return lease
}

func createAgentRunForTest(
	ctx context.Context,
	t *testing.T,
	store *Store,
	sessionID uuid.UUID,
	parentTurnID uuid.UUID,
	index int,
) *models.AgentRun {
	t.Helper()

	run, err := store.CreateAgentRun(ctx, sessionID, StartAgentRunInput{
		ID:               uuid.New(),
		ParentTurnID:     parentTurnID,
		RequestID:        uuid.New(),
		Name:             fmt.Sprintf("child-%02d", index),
		Definition:       models.AgentRunDefinitionStored,
		Depth:            1,
		Workspace:        testWorkspace,
		ModelReference:   agentRunTestModel,
		ModelID:          agentRunTestModel,
		Task:             agentRunTestTask,
		Instructions:     agentRunTestInstructions,
		AllowedToolsJSON: `[]`,
		SystemPrompt:     agentRunTestPrompt,
	})
	require.NoError(t, err)

	return run
}

func agentRunEventSequences(items []*models.AgentRunEvent) []int64 {
	sequences := make([]int64, 0, len(items))
	for _, item := range items {
		sequences = append(sequences, item.Sequence)
	}

	return sequences
}

func agentRunIDs(items []*models.AgentRun) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}

	return ids
}
