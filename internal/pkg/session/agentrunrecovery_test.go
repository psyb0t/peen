package session

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaimAgentRunPreservesIdentityAndRejectsDuplicateOwner(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		state     models.AgentRunState
		cancelled bool
		wantErr   error
	}{
		{name: "interrupted", state: models.AgentRunStateInterrupted},
		{name: "stale running", state: models.AgentRunStateRunning},
		{name: "completed", state: models.AgentRunStateCompleted, wantErr: commerr.ErrConflict},
		{name: "failed", state: models.AgentRunStateFailed, wantErr: commerr.ErrConflict},
		{name: "cancelled", state: models.AgentRunStateCancelled, wantErr: commerr.ErrConflict},
		{name: "cancellation pending", state: models.AgentRunStateInterrupted, cancelled: true, wantErr: commerr.ErrConflict},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store, handle := openTestStore(t)
			t.Cleanup(func() { require.NoError(t, handle.Close()) })
			opened := newTestSession(ctx, t, store)
			lease := startAgentRunTestTurn(ctx, t, store, opened.ID)
			run := createAgentRunForTest(ctx, t, store, opened.ID, lease.TurnID, 1)
			repository := store.query.AgentRun
			_, err := repository.WithContext(ctx).Where(repository.ID.Eq(run.ID)).UpdateSimple(repository.State.Value(string(tc.state)), repository.CancelRequested.Value(tc.cancelled))
			require.NoError(t, err)
			input := ClaimAgentRunInput{GenerationID: uuid.New(), PreviousGenerationID: run.WorkerGenerationID, PreviousState: tc.state}
			claimed, err := store.ClaimAgentRun(ctx, opened.ID, run.ID, input)
			require.ErrorIs(t, err, tc.wantErr)
			if tc.wantErr != nil {
				assert.Nil(t, claimed)

				return
			}
			assert.Equal(t, run.ID, claimed.ID)
			assert.Equal(t, run.Task, claimed.Task)
			assert.Equal(t, run.StartedAt, claimed.StartedAt)
			assert.Equal(t, input.GenerationID.String(), claimed.WorkerGenerationID)
			assert.Equal(t, models.AgentRunStateRunning, claimed.State)
			assert.Nil(t, claimed.EndedAt)
			_, err = store.ClaimAgentRun(ctx, opened.ID, run.ID, input)
			require.ErrorIs(t, err, commerr.ErrConflict)
			input.GenerationID = uuid.New()
			_, err = store.ClaimAgentRun(ctx, opened.ID, run.ID, input)
			require.ErrorIs(t, err, commerr.ErrConflict)
			_, err = store.ClaimAgentRun(ctx, uuid.New(), run.ID, input)
			require.ErrorIs(t, err, commerr.ErrNotFound)
		})
	}
}

func TestPrepareAgentRunRecoveryOnlyInterruptsTheReplacedSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	opened := newTestSession(ctx, t, store)
	other := newTestSession(ctx, t, store)
	lease := startAgentRunTestTurn(ctx, t, store, opened.ID)
	otherLease := startAgentRunTestTurn(ctx, t, store, other.ID)
	stale := createAgentRunForTest(ctx, t, store, opened.ID, lease.TurnID, 1)
	current := createAgentRunForTest(ctx, t, store, opened.ID, lease.TurnID, 2)
	untouched := createAgentRunForTest(ctx, t, store, other.ID, otherLease.TurnID, 3)
	generationID := uuid.New()
	repository := store.query.AgentRun
	_, err := repository.WithContext(ctx).Where(repository.ID.Eq(current.ID)).UpdateSimple(repository.WorkerGenerationID.Value(generationID.String()))
	require.NoError(t, err)
	count, err := store.PrepareAgentRunRecovery(ctx, opened.ID, generationID)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	testCases := []struct {
		name  string
		run   *models.AgentRun
		state models.AgentRunState
	}{
		{name: "stale", run: stale, state: models.AgentRunStateInterrupted},
		{name: "current", run: current, state: models.AgentRunStateRunning},
		{name: "other session", run: untouched, state: models.AgentRunStateRunning},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stored, err := store.GetAgentRun(ctx, tc.run.SessionID, tc.run.ID)
			require.NoError(t, err)
			assert.Equal(t, tc.state, stored.State)
		})
	}
	count, err = store.PrepareAgentRunRecovery(ctx, opened.ID, generationID)
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestReplacedWorkerCannotAppendChildCheckpoints(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	opened := newTestSession(ctx, t, store)
	lease := startAgentRunTestTurn(ctx, t, store, opened.ID)
	run := createAgentRunForTest(ctx, t, store, opened.ID, lease.TurnID, 1)
	owner := uuid.New()
	_, err := store.ClaimAgentRun(ctx, opened.ID, run.ID, ClaimAgentRunInput{GenerationID: owner, PreviousState: run.State, PreviousGenerationID: run.WorkerGenerationID})
	require.NoError(t, err)
	_, err = store.AppendAgentRunMessages(WithAgentRunOwner(ctx, uuid.New()), opened.ID, run.ID, []AgentRunMessageInput{{Role: models.MessageRoleUser, Content: "stale"}})
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	_, err = store.AppendAgentRunMessages(WithAgentRunOwner(ctx, owner), opened.ID, run.ID, []AgentRunMessageInput{{Role: models.MessageRoleUser, Content: "current"}})
	require.NoError(t, err)
	history, err := store.AgentRunHistory(ctx, opened.ID, run.ID)
	require.NoError(t, err)
	require.Len(t, history.Messages, 1)
	assert.Equal(t, "current", history.Messages[0].Content)
}

func TestInterruptedChildCancellationPreventsARecoveryClaim(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	opened := newTestSession(ctx, t, store)
	lease := startAgentRunTestTurn(ctx, t, store, opened.ID)
	run := createAgentRunForTest(ctx, t, store, opened.ID, lease.TurnID, 1)
	_, err := store.RecoverInterruptedAgentRuns(ctx)
	require.NoError(t, err)
	_, requested, err := store.RequestAgentRunCancellation(ctx, opened.ID, run.ID)
	require.NoError(t, err)
	assert.True(t, requested)
	_, err = store.ClaimAgentRun(ctx, opened.ID, run.ID, ClaimAgentRunInput{GenerationID: uuid.New(), PreviousState: models.AgentRunStateInterrupted, PreviousGenerationID: run.WorkerGenerationID})
	require.ErrorIs(t, err, commerr.ErrConflict)
}
