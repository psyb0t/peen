package session

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCancelRequestLeavesAnotherActiveTurnUntouched(t *testing.T) {
	t.Parallel()
	store, sessionID, _ := openingNoticeFixture(t)
	requestID := uuid.New()
	lease, err := store.AcquireTurn(t.Context(), sessionID,
		StartTurnInput{RequestID: requestID, Workspace: testWorkspace})
	require.NoError(t, err)
	t.Cleanup(func() { store.ReleaseTurn(lease) })
	var cancelled atomic.Bool
	store.RegisterCancellation(lease, func() { cancelled.Store(true) })
	turnID, err := store.CancelRequest(t.Context(), sessionID, uuid.New())
	require.NoError(t, err)
	assert.Equal(t, uuid.Nil, turnID)
	assert.False(t, cancelled.Load())
	turn, err := store.GetTurnByRequest(t.Context(), sessionID, requestID)
	require.NoError(t, err)
	assert.False(t, turn.CancelRequested)
	assert.Equal(t, models.TurnStateRunning, turn.State)
	stored, err := store.Get(t.Context(), sessionID)
	require.NoError(t, err)
	assert.Zero(t, stored.StopRevision)
	turnID, err = store.CancelRequest(t.Context(), sessionID, requestID)
	require.NoError(t, err)
	assert.Equal(t, lease.TurnID, turnID)
	assert.True(t, cancelled.Load())
	turn, err = store.GetTurnByRequest(t.Context(), sessionID, requestID)
	require.NoError(t, err)
	assert.True(t, turn.CancelRequested)
	_, err = store.GetTurnByRequest(t.Context(), uuid.New(), requestID)
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

func TestCancelRequestIgnoresUnknownAndTerminalRequests(t *testing.T) {
	t.Parallel()
	store, sessionID, _ := openingNoticeFixture(t)
	requestID := uuid.New()
	turnID, err := store.CancelRequest(t.Context(), sessionID, requestID)
	require.NoError(t, err)
	assert.Equal(t, uuid.Nil, turnID)
	lease, err := store.AcquireTurn(t.Context(), sessionID,
		StartTurnInput{RequestID: requestID, Workspace: testWorkspace})
	require.NoError(t, err)
	require.NoError(t, store.FinalizeTurn(t.Context(), lease,
		FinalizeTurnInput{State: models.TurnStateCompleted}))
	turnID, err = store.CancelRequest(t.Context(), sessionID, requestID)
	require.NoError(t, err)
	assert.Equal(t, uuid.Nil, turnID)
	stored, err := store.Get(t.Context(), sessionID)
	require.NoError(t, err)
	assert.Zero(t, stored.StopRevision)
	_, err = store.CancelRequest(t.Context(), sessionID, uuid.Nil)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

func TestCancelRequestPreservesChildrenOfAnEarlierTurn(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	oldLease := inboxTestLease(t, fixture)
	oldChild := executionChildForTest(t, fixture, fixture.root)
	_, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, oldChild.ID, uuid.New(), ScheduleActivityInput{
			StepID: "model:0", Name: "oldChildModel",
			Kind:         models.ActivityKindModel,
			ReplayPolicy: models.ActivityReplayPolicyRetry,
			Input:        json.RawMessage(`{}`),
		})
	require.NoError(t, err)
	finishRootForTest(t, fixture)
	require.NoError(t, fixture.store.FinalizeTurn(t.Context(), oldLease,
		FinalizeTurnInput{State: models.TurnStateCompleted}))
	before, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		oldChild.ID)
	require.NoError(t, err)
	requestID := uuid.New()
	lease, err := fixture.store.AcquireTurn(t.Context(), fixture.sessionID,
		StartTurnInput{RequestID: requestID, Workspace: testWorkspace})
	require.NoError(t, err)
	t.Cleanup(func() { fixture.store.ReleaseTurn(lease) })
	current, err := fixture.store.CreateExecution(t.Context(), fixture.sessionID,
		executionTestInput(lease.TurnID))
	require.NoError(t, err)
	turnID, err := fixture.store.CancelRequest(t.Context(), fixture.sessionID,
		requestID)
	require.NoError(t, err)
	assert.Equal(t, lease.TurnID, turnID)
	paused, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		current.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, paused.State)
	after, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		oldChild.ID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}
