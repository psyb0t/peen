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
	modelRunTestReference       = "gateway/test-model"
	modelRunTestSettingsJSON    = `{"streaming":true}`
	modelRunTestMessagesJSON    = `[{"role":"user","content":"inspect"}]`
	modelRunTestToolsJSON       = `[{"name":"read_file"}]`
	modelRunTestResponseJSON    = `{"role":"assistant","content":"done"}`
	modelRunTestUsageJSON       = `{"prompt":12,"completion":8,"total":20}`
	modelRunTestRetryJSON       = `[{"attempt":1,"reason":"rate_limit"}]`
	modelRunTestRunMessagesJSON = `[{"role":"assistant","content":"done"}]`
)

func TestStoreModelRunReplayPagesAccountingAndRecovery(t *testing.T) {
	ctx := context.Background()
	stateDirectory := filepath.Join(t.TempDir(), "state")
	handle, err := db.Open(ctx, db.Config{Directory: stateDirectory})
	require.NoError(t, err)

	store := newTestStore(t, handle)
	owner := newTestSession(ctx, t, store)
	other := newTestSession(ctx, t, store)
	lease := startAgentRunTestTurn(ctx, t, store, owner.ID)

	runs := make([]*models.ModelRun, 0, 13)
	for index := range 13 {
		run := createModelRunForTest(ctx, t, store, owner.ID, lease.TurnID, index)
		call := createModelCallForTest(ctx, t, store, owner.ID, run.ID, index)
		if index == 0 {
			require.NoError(t, store.RecordModelCallRetries(
				ctx,
				owner.ID,
				run.ID,
				call.ID,
				RecordModelCallRetriesInput{
					RetryAttemptsJSON: modelRunTestRetryJSON,
					RetryAttemptCount: 1,
				},
			))
		}
		_, err = store.FinalizeModelCall(ctx, owner.ID, run.ID, call.ID, FinalizeModelCallInput{
			State:                   models.ModelCallStateCompleted,
			ResponseMessageJSON:     modelRunTestResponseJSON,
			ResponseUsageJSON:       modelRunTestUsageJSON,
			RetryAttemptsJSON:       retryJSONForModelRunTest(index),
			RetryAttemptCount:       retryCountForModelRunTest(index),
			PromptTokens:            12,
			CompletionTokens:        8,
			TotalTokens:             20,
			ReasoningTokens:         4,
			CacheReadTokens:         3,
			CacheWriteTokens:        2,
			CacheWriteLongTTLTokens: 1,
			WastedPromptTokens:      retryCountForModelRunTest(index),
			WastedCompletionTokens:  retryCountForModelRunTest(index),
			WastedTotalTokens:       retryCountForModelRunTest(index) * 2,
			TotalAttempts:           1 + retryCountForModelRunTest(index),
			ResponseModelID:         "served-model",
			FinishReason:            "stop",
			ResponseCostAmount:      "0.00020",
			RetryCostAmount:         "0.00007",
			BilledCostAmount:        "0.00027",
			CostKnown:               true,
		})
		require.NoError(t, err)
		_, err = store.FinalizeModelRun(ctx, owner.ID, run.ID, FinalizeModelRunInput{
			State:                  models.ModelRunStateCompleted,
			ResponseModelID:        "served-model",
			ResponseText:           "done",
			ResponseThinking:       "checked",
			ResponseMessagesJSON:   modelRunTestRunMessagesJSON,
			ResponseInjectionsJSON: `[]`,
			ResponseUsageJSON:      modelRunTestUsageJSON,
			ResponseCostAmount:     "0.00020",
			RetryCostAmount:        "0.00007",
			BilledCostAmount:       "0.00027",
			CostKnown:              true,
			FinishReason:           "stop",
		})
		require.NoError(t, err)
		runs = append(runs, run)
	}

	running := createModelRunForTest(ctx, t, store, owner.ID, lease.TurnID, 99)
	runningCall := createModelCallForTest(ctx, t, store, owner.ID, running.ID, 99)

	var listed []uuid.UUID
	for offset := 0; ; offset += 5 {
		page, pageErr := store.ListModelRuns(ctx, owner.ID, ListModelRunsOptions{
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
	require.Len(t, listed, len(runs)+1)
	assert.Contains(t, listed, running.ID)
	assert.ElementsMatch(t, append(modelRunIDs(runs), running.ID), listed)

	calls, err := store.ListModelCalls(ctx, owner.ID, runs[0].ID, ListModelCallsOptions{Limit: 1})
	require.NoError(t, err)
	require.Len(t, calls.Items, 1)
	assert.Equal(t, int64(0), calls.Items[0].Round)
	assert.Equal(t, modelRunTestMessagesJSON, calls.Items[0].RequestMessagesJSON)
	assert.Equal(t, modelRunTestToolsJSON, calls.Items[0].RequestToolsJSON)
	assert.Equal(t, int64(1), calls.Items[0].RetryAttemptCount)
	assert.Equal(t, int64(12), calls.Items[0].PromptTokens)
	assert.Equal(t, int64(4), calls.Items[0].ReasoningTokens)
	assert.Equal(t, "0.00027", calls.Items[0].BilledCostAmount)
	assert.True(t, calls.Items[0].CostKnown)

	_, err = store.GetModelRun(ctx, other.ID, runs[0].ID)
	require.ErrorIs(t, err, commerr.ErrNotFound)
	_, err = store.ListModelCalls(ctx, other.ID, runs[0].ID, ListModelCallsOptions{Limit: 1})
	require.ErrorIs(t, err, commerr.ErrNotFound)

	require.NoError(t, handle.Close())
	reopenedHandle, err := db.Open(ctx, db.Config{Directory: stateDirectory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopenedHandle.Close()) })
	reopened := newTestStore(t, reopenedHandle)

	recovered, err := reopened.RecoverInterruptedModelRuns(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, recovered)
	interrupted, err := reopened.GetModelRun(ctx, owner.ID, running.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ModelRunStateInterrupted, interrupted.State)
	assert.Contains(t, interrupted.FailureDetail, "process stopped")
	interruptedCalls, err := reopened.ListModelCalls(
		ctx,
		owner.ID,
		running.ID,
		ListModelCallsOptions{Limit: 1},
	)
	require.NoError(t, err)
	require.Len(t, interruptedCalls.Items, 1)
	assert.Equal(t, runningCall.ID, interruptedCalls.Items[0].ID)
	assert.Equal(t, models.ModelCallStateInterrupted, interruptedCalls.Items[0].State)
	assert.Zero(t, mustRecoverModelRuns(t, reopened, ctx))
}

func createModelRunForTest(
	ctx context.Context,
	t *testing.T,
	store *Store,
	sessionID uuid.UUID,
	turnID uuid.UUID,
	index int,
) *models.ModelRun {
	t.Helper()

	run, err := store.CreateModelRun(ctx, sessionID, CreateModelRunInput{
		ID:                  uuid.New(),
		TurnID:              turnID,
		Stage:               models.ModelRunStageTurn,
		ModelReference:      modelRunTestReference,
		ConnectionName:      "gateway",
		RequestedModelID:    fmt.Sprintf("test-model-%02d", index),
		RequestSettingsJSON: modelRunTestSettingsJSON,
	})
	require.NoError(t, err)

	return run
}

func createModelCallForTest(
	ctx context.Context,
	t *testing.T,
	store *Store,
	sessionID uuid.UUID,
	runID uuid.UUID,
	index int,
) *models.ModelCall {
	t.Helper()

	call, err := store.CreateModelCall(ctx, sessionID, runID, CreateModelCallInput{
		ID:                  uuid.New(),
		Round:               int64(index),
		RequestMessagesJSON: modelRunTestMessagesJSON,
		RequestToolsJSON:    modelRunTestToolsJSON,
	})
	require.NoError(t, err)

	return call
}

func retryJSONForModelRunTest(index int) string {
	if index == 0 {
		return modelRunTestRetryJSON
	}

	return `[]`
}

func retryCountForModelRunTest(index int) int64 {
	if index == 0 {
		return 1
	}

	return 0
}

func modelRunIDs(items []*models.ModelRun) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}

	return ids
}

func mustRecoverModelRuns(t *testing.T, store *Store, ctx context.Context) int {
	t.Helper()

	recovered, err := store.RecoverInterruptedModelRuns(ctx)
	require.NoError(t, err)

	return recovered
}
