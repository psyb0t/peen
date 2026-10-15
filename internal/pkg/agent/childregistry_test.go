package agent

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChildRegistryRestoresOriginalIdentityWithoutLaunching(t *testing.T) {
	t.Parallel()
	fixture, deps, _, run, _ := initialChildConversationFixture(t)
	stored, err := fixture.base.store.GetAgentRun(t.Context(), deps.sessionID,
		run.ID)
	require.NoError(t, err)
	registry, err := NewAgentRunRegistry(deps.sessionID, nil, AgentRunLimits{})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	restored, err := registry.restoreWorkflowRun(stored, cancel, nil)
	require.NoError(t, err)
	assert.Equal(t, stored.ID, restored.ID)
	assert.Equal(t, stored.ParentTurnID, restored.ParentTurnID)
	assert.Equal(t, stored.StartedAt, restored.StartedAt)
	assert.Equal(t, stored.Depth, int64(restored.Depth))
	assert.NoError(t, ctx.Err())
	again, err := registry.restoreWorkflowRun(stored, cancel, nil)
	require.NoError(t, err)
	assert.Same(t, restored, again)
	assert.Len(t, registry.List(), 1)
	current, err := fixture.base.store.GetAgentRun(t.Context(), deps.sessionID,
		run.ID)
	require.NoError(t, err)
	assert.Equal(t, stored, current)
	_, found := registry.Cancel(restored.ID)
	assert.True(t, found)
	assert.ErrorIs(t, ctx.Err(), context.Canceled)
}

func TestChildRegistryRetainsTerminalStateAndBoundedStoredEvents(t *testing.T) {
	t.Parallel()
	fixture, deps, _, original, _ := initialChildConversationFixture(t)
	stored, err := fixture.base.store.FinalizeAgentRun(t.Context(), deps.sessionID,
		original.ID, session.FinalizeAgentRunInput{
			State: models.AgentRunStateCompleted, ResponseText: "saved response",
		})
	require.NoError(t, err)
	registry, err := NewAgentRunRegistry(deps.sessionID, nil, AgentRunLimits{
		MaxEventCount: 2,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	storedEvents := []AgentRunEvent{
		{Sequence: 1, Type: EventTypeAgentRunStarted},
		{Sequence: 2, Type: EventTypeAgentRunTextDelta},
		{Sequence: 3, Type: EventTypeAgentRunCompleted},
	}
	run, err := registry.restoreWorkflowRun(stored, cancel, storedEvents)
	require.NoError(t, err)
	snapshot := run.Snapshot()
	assert.Equal(t, AgentRunStateCompleted, snapshot.State)
	assert.Equal(t, *stored.EndedAt, snapshot.EndedAt)
	assert.Equal(t, 2, snapshot.EventBufferedCount)
	assert.Equal(t, 1, snapshot.EventDroppedCount)
	select {
	case <-run.Done():
	default:
		t.Fatal("restored terminal run has an open completion channel")
	}
	_, found := registry.Cancel(run.ID)
	assert.True(t, found)
	assert.NoError(t, ctx.Err())
}

func TestChildRegistryRejectsMalformedAndForeignRecords(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		change  func(*models.AgentRun)
		wantErr error
	}{
		{
			name: "foreign session", wantErr: commerr.ErrPermissionDenied,
			change: func(run *models.AgentRun) { run.SessionID = uuid.New() },
		},
		{
			name: "missing ID", wantErr: commerr.ErrValidationFailed,
			change: func(run *models.AgentRun) { run.ID = uuid.Nil },
		},
		{
			name: "missing depth", wantErr: commerr.ErrValidationFailed,
			change: func(run *models.AgentRun) { run.Depth = 0 },
		},
		{
			name: "unknown state", wantErr: commerr.ErrValidationFailed,
			change: func(run *models.AgentRun) {
				run.State = models.AgentRunState("unknown")
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, deps, _, original, _ := initialChildConversationFixture(t)
			stored, err := fixture.base.store.GetAgentRun(t.Context(), deps.sessionID,
				original.ID)
			require.NoError(t, err)
			registry, err := NewAgentRunRegistry(deps.sessionID, nil, AgentRunLimits{})
			require.NoError(t, err)
			_, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			tc.change(stored)
			_, err = registry.restoreWorkflowRun(stored, cancel, nil)
			require.ErrorIs(t, err, tc.wantErr)
			assert.Empty(t, registry.List())
		})
	}
}

func TestChildRegistryPreservesPausedExecutionAndRejectsChangedEntry(
	t *testing.T,
) {
	t.Parallel()
	fixture, input, sink := childModelFixture(t)
	_, err := fixture.base.store.PauseExecution(t.Context(), sink.sessionID,
		input.Execution.ExecutionID, "leave child paused")
	require.NoError(t, err)
	before, err := fixture.base.store.GetExecution(t.Context(), sink.sessionID,
		input.Execution.ExecutionID)
	require.NoError(t, err)
	stored, err := fixture.base.store.GetAgentRun(t.Context(), sink.sessionID,
		sink.run.ID)
	require.NoError(t, err)
	registry, err := NewAgentRunRegistry(sink.sessionID, nil, AgentRunLimits{})
	require.NoError(t, err)
	_, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	_, err = registry.restoreWorkflowRun(nil, cancel, nil)
	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
	_, err = registry.restoreWorkflowRun(stored, nil, nil)
	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
	run, err := registry.restoreWorkflowRun(stored, cancel, nil)
	require.NoError(t, err)
	stored.Name = "another child identity"
	_, err = registry.restoreWorkflowRun(stored, cancel, nil)
	require.ErrorIs(t, err, commerr.ErrConflict)
	assert.NotEqual(t, stored.Name, run.Name)
	after, err := fixture.base.store.GetExecution(t.Context(), sink.sessionID,
		input.Execution.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.Equal(t, models.ExecutionStatePaused, after.State)
}
