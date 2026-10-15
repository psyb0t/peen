package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func childPreparationScopeForTest(
	t *testing.T,
	fixture executionLoopFixture,
	conversation workflowConversation,
) *directoryScope {
	t.Helper()
	saved := conversation.Child
	_, prepared, err := fixture.base.runtime.restoreChildWorkflowScope(
		t.Context(), &launchAgentDeps{
			sessionID: saved.SessionID, parentTurnID: saved.TurnID,
			requestID: saved.RequestID,
		}, &AgentRun{ID: saved.AgentRunID}, conversation)
	require.NoError(t, err)

	return prepared.scope
}

func TestChildWorkflowPreparationRetainsNewReadProofs(t *testing.T) {
	t.Parallel()
	fixture, input, original, _ := childWorkflowInboxFixture(t)
	scope := childPreparationScopeForTest(t, fixture, original)
	path := filepath.Join(fixture.base.workspace, "child-source.txt")
	writeRuntimeFile(t, path, "child source")
	_, err := scope.context().executor.ReadFile(t.Context(), tools.ReadFileInput{
		Path: path,
	})
	require.NoError(t, err)
	encoded, err := fixture.base.runtime.prepareChildWorkflowRound(t.Context(),
		scope, input)
	require.NoError(t, err)
	var prepared workflowConversation
	require.NoError(t, json.Unmarshal(encoded, &prepared))
	assert.Equal(t, scope.context().executor.SnapshotObservations(),
		prepared.Runtime.Observations)
	assert.Empty(t, original.Runtime.Observations)
}

func TestChildWorkflowPreparationRejectsForeignScopeAndMalformedContext(
	t *testing.T,
) {
	t.Parallel()
	fixture, input, original, _ := childWorkflowInboxFixture(t)
	scope := childPreparationScopeForTest(t, fixture, original)
	_, err := fixture.base.runtime.prepareChildWorkflowRound(t.Context(), nil,
		input)
	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
	foreign := uuid.New()
	scope.options.agentRunID = &foreign
	_, err = fixture.base.runtime.prepareChildWorkflowRound(t.Context(), scope,
		input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	assert.NotContains(t, scope.context().skills.held(), "childInboxSkill")
	input.Execution.Context = json.RawMessage(`{`)
	_, err = fixture.base.runtime.prepareChildWorkflowRound(t.Context(), scope,
		input)
	require.Error(t, err)
}

func TestChildWorkflowPreparationSnapshotsScopeAndAcceptedInbox(t *testing.T) {
	t.Parallel()
	fixture, input, original, _ := childWorkflowInboxFixture(t)
	scope := childPreparationScopeForTest(t, fixture, original)
	encoded, err := fixture.base.runtime.prepareChildWorkflowRound(t.Context(),
		scope, input)
	require.NoError(t, err)
	var prepared workflowConversation
	require.NoError(t, json.Unmarshal(encoded, &prepared))
	assert.Equal(t, original.Child, prepared.Child)
	assert.Equal(t, original.SystemPrompt, prepared.SystemPrompt)
	assert.Equal(t, original.Runtime.Directory, prepared.Runtime.Directory)
	require.Len(t, prepared.InboxIDs, 1)
	require.Len(t, prepared.Messages, 3)
	assert.Contains(t, prepared.Runtime.Skills.Delivered, "childInboxSkill")
	assert.NotContains(t, prepared.Runtime.Skills.Delivered, "parent-only")
	snapshot, err := harness.RestoreCheckpoint(prepared.Runtime.Harness)
	require.NoError(t, err)
	assert.Equal(t, scope.context().snapshot.Hash(), snapshot.Hash())
	expectedTools, err := json.Marshal(
		snapshotWorkflowToolDefinitions(scope.context().tools))
	require.NoError(t, err)
	actualTools, err := json.Marshal(prepared.Tools)
	require.NoError(t, err)
	assert.JSONEq(t, string(expectedTools), string(actualTools))
}

func TestChildWorkflowPreparationCheckpointReusesAcceptedInput(t *testing.T) {
	t.Parallel()
	fixture, input, original, _ := childWorkflowInboxFixture(t)
	scope := childPreparationScopeForTest(t, fixture, original)
	fixture.engine.options.PrepareRound = func(
		ctx context.Context,
		round workflowRoundInput,
	) (json.RawMessage, error) {
		return fixture.base.runtime.prepareChildWorkflowRound(ctx, scope, round)
	}
	first, err := fixture.engine.prepareRound(t.Context(), input)
	require.NoError(t, err)
	_, err = fixture.base.store.AcceptChildExecutionMessage(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID,
		session.AcceptExecutionMessageInput{
			RequestID: uuid.New(), WorkingDirectory: fixture.base.workspace,
			Message: "arrival after preparation", Capacity: 3,
		})
	require.NoError(t, err)
	reused, err := fixture.engine.prepareRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	assert.JSONEq(t, string(first.Output), string(reused.Output))
	var saved workflowConversation
	require.NoError(t, json.Unmarshal(reused.Output, &saved))
	require.Len(t, saved.InboxIDs, 1)
	assert.Len(t, saved.Messages, 3)
}
