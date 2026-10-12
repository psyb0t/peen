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

func TestGetDirectoryStateRequiresSuccessfulPairedCalls(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name            string
		calls           string
		role            models.MessageRole
		isError         bool
		wrongTurn       bool
		wrongSession    bool
		callAfterResult bool
		workspace       string
		want            string
	}{
		{name: "accepted", calls: `[{"id":"hop","name":"changeDirectory"}]`, role: models.MessageRoleTool, workspace: "nested", want: "nested"},
		{name: "scalar JSON", calls: `["hop",42,null]`, role: models.MessageRoleTool, workspace: "nested"},
		{name: "wrong call ID", calls: `[{"id":"other","name":"changeDirectory"}]`, role: models.MessageRoleTool, workspace: "nested"},
		{name: "other tool", calls: `[{"id":"hop","name":"readFile"}]`, role: models.MessageRoleTool, workspace: "nested"},
		{name: "unpaired", calls: `[]`, role: models.MessageRoleTool, workspace: "nested"},
		{name: "failed", calls: `[{"id":"hop","name":"changeDirectory"}]`, role: models.MessageRoleTool, workspace: "nested", isError: true},
		{name: "user spoof", calls: `[{"id":"hop","name":"changeDirectory"}]`, role: models.MessageRoleUser, workspace: "nested"},
		{name: "empty directory", calls: `[{"id":"hop","name":"changeDirectory"}]`, role: models.MessageRoleTool},
		{name: "other turn", calls: `[{"id":"hop","name":"changeDirectory"}]`, role: models.MessageRoleTool, workspace: "nested", wrongTurn: true},
		{name: "other session", calls: `[{"id":"hop","name":"changeDirectory"}]`, role: models.MessageRoleTool, workspace: "nested", wrongSession: true},
		{name: "future call", calls: `[{"id":"hop","name":"changeDirectory"}]`, role: models.MessageRoleTool, workspace: "nested", callAfterResult: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store, handle := openTestStore(t)
			t.Cleanup(func() { require.NoError(t, handle.Close()) })
			opened := newTestSession(ctx, t, store)
			lease, err := store.AcquireTurn(ctx, opened.ID, StartTurnInput{RequestID: uuid.New(), Workspace: testWorkspace})
			require.NoError(t, err)
			resultTurnID := lease.TurnID
			resultSessionID := opened.ID
			if tc.wrongTurn || tc.wrongSession {
				other := opened
				if tc.wrongSession {
					other = newTestSession(ctx, t, store)
				} else {
					require.NoError(t, store.FinalizeTurn(ctx, lease, FinalizeTurnInput{State: models.TurnStateCompleted}))
				}
				second, acquireErr := store.AcquireTurn(ctx, other.ID, StartTurnInput{RequestID: uuid.New(), Workspace: testWorkspace})
				require.NoError(t, acquireErr)
				resultTurnID = second.TurnID
				resultSessionID = other.ID
			}
			callSequence, resultSequence := int64(1), int64(2)
			if tc.callAfterResult {
				callSequence, resultSequence = resultSequence, callSequence
			}
			require.NoError(t, store.query.Message.WithContext(ctx).Create(
				&models.Message{ID: uuid.New(), SessionID: opened.ID, TurnID: lease.TurnID, Sequence: callSequence, Role: models.MessageRoleAssistant, ToolCallsJSON: tc.calls},
				&models.Message{ID: uuid.New(), SessionID: resultSessionID, TurnID: resultTurnID, Sequence: resultSequence, Role: tc.role, ToolCallsJSON: "[]", ToolCallID: "hop", Workspace: tc.workspace, IsError: tc.isError, Content: "truncated"},
			))
			state, err := store.GetDirectoryState(ctx, opened.ID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, state.Directory)
			if tc.want != "" {
				assert.Equal(t, resultSequence, state.Sequence)
			} else {
				assert.Zero(t, state.Sequence)
			}
		})
	}
}

func TestGetDirectoryStateMissingAndCancelled(t *testing.T) {
	t.Parallel()
	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	ctx := context.Background()
	_, err := store.GetDirectoryState(ctx, uuid.New())
	require.ErrorIs(t, err, commerr.ErrNotFound)
	opened := newTestSession(ctx, t, store)
	state, err := store.GetDirectoryState(ctx, opened.ID)
	require.NoError(t, err)
	assert.Equal(t, &DirectoryState{}, state)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = store.GetDirectoryState(cancelled, opened.ID)
	require.ErrorIs(t, err, context.Canceled)
}

//nolint:tparallel // Subtests append to one ordered transcript.
func TestDirectoryStatePairsReusedIDsWithTheClosestCall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	opened := newTestSession(ctx, t, store)
	lease, err := store.AcquireTurn(ctx, opened.ID, StartTurnInput{
		RequestID: uuid.New(), Workspace: testWorkspace,
	})
	require.NoError(t, err)
	testCases := []struct {
		name         string
		calls        string
		workspace    string
		want         string
		wantSequence int64
	}{
		{name: "first success", calls: `[{"ID":"reused","Name":"changeDirectory"}]`, workspace: "first", want: "first", wantSequence: 2},
		{name: "unrelated reused call", calls: `[{"ID":"reused","Name":"readFile"}]`, workspace: "spoofed", want: "first", wantSequence: 2},
		{name: "latest success", calls: `[{"ID":"reused","Name":"changeDirectory"}]`, workspace: "latest", want: "latest", wantSequence: 6},
	}
	for index, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sequence := int64(index*2 + 1)
			require.NoError(t, store.query.Message.WithContext(ctx).Create(
				&models.Message{ID: uuid.New(), SessionID: opened.ID, TurnID: lease.TurnID, Sequence: sequence, Role: models.MessageRoleAssistant, ToolCallsJSON: tc.calls},
				&models.Message{ID: uuid.New(), SessionID: opened.ID, TurnID: lease.TurnID, Sequence: sequence + 1, Role: models.MessageRoleTool, ToolCallID: "reused", ToolCallsJSON: "[]", Workspace: tc.workspace},
			))
			state, err := store.GetDirectoryState(ctx, opened.ID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, state.Directory)
			assert.Equal(t, tc.wantSequence, state.Sequence)
		})
	}
}
