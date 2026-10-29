package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChildRestorationKeepsFrozenScopeAndIndependentReadProofs(t *testing.T) {
	t.Parallel()
	fixture, deps, launch, run, prepared := initialChildConversationFixture(t)
	path := filepath.Join(deps.executor.Workspace(), "child.txt")
	writeRuntimeFile(t, path, "original child source")
	_, err := deps.executor.ReadFile(t.Context(), tools.ReadFileInput{
		Path: "child.txt",
	})
	require.NoError(t, err)
	conversation, err := fixture.base.runtime.childWorkflowConversation(
		t.Context(), deps, launch, run, prepared)
	require.NoError(t, err)
	writeRuntimeFile(t, filepath.Join(deps.executor.Workspace(), "AGENTS.md"),
		"changed instructions after checkpoint")
	launch.skillLedger.record("later-child-skill")
	restoredDeps, restored, err := fixture.base.runtime.restoreChildWorkflowScope(
		t.Context(), deps, run, conversation)
	require.NoError(t, err)
	current := restored.scope.context()
	assert.Equal(t, conversation.SystemPrompt, current.prompt)
	assert.Equal(t, conversation.Runtime.Directory.ContextHash,
		current.contextHash)
	assert.Equal(t, conversation.Runtime.Observations,
		restoredDeps.executor.SnapshotObservations())
	assert.Contains(t, current.skills.held(), "child-only")
	assert.NotContains(t, current.skills.held(), "later-child-skill")
	assert.NotContains(t, current.skills.held(), "parent-only")
	assert.NotSame(t, deps.executor, restoredDeps.executor)
	assert.Equal(t, prepared.maximumRounds, restored.maximumRounds)
}

func TestChildRestorationKeepsPendingHookState(t *testing.T) {
	t.Parallel()
	fixture, deps, launch, run, prepared := initialChildConversationFixture(t)
	prepared.scope.context().hooks.add(hookInjectTestCallID,
		[]string{hookInjectTestNote})
	prepared.scope.context().hooks.deny(&elelem.ToolEvent{CallID: "deniedCall"},
		"retained child denial")
	conversation, err := fixture.base.runtime.childWorkflowConversation(
		t.Context(), deps, launch, run, prepared)
	require.NoError(t, err)
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	var saved workflowConversation
	require.NoError(t, json.Unmarshal(encoded, &saved))
	_, restored, err := fixture.base.runtime.restoreChildWorkflowScope(
		t.Context(), deps, run, saved)
	require.NoError(t, err)
	hooks := restored.scope.context().hooks
	injection, err := hooks.messageInjection(t.Context(),
		&elelem.ToolEvent{CallID: hookInjectTestCallID})
	require.NoError(t, err)
	require.NotNil(t, injection)
	assert.Equal(t, hookInjectTestNote, injection.Content)
	denial, denied := hooks.takeDenial("deniedCall")
	assert.True(t, denied)
	assert.Equal(t, "retained child denial", denial)
}

func TestChildRestorationPreservesPausedStateAfterDatabaseReopen(t *testing.T) {
	t.Parallel()
	fixture, deps, launch, run, prepared := initialChildConversationFixture(t)
	conversation, err := fixture.base.runtime.childWorkflowConversation(
		t.Context(), deps, launch, run, prepared)
	require.NoError(t, err)
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	execution, err := fixture.base.store.CreateExecution(t.Context(),
		deps.sessionID, session.CreateExecutionInput{
			ID: uuid.New(), RootTurnID: deps.parentTurnID,
			ParentExecutionID: &fixture.input.ExecutionID, AgentRunID: &run.ID,
			WorkflowVersion: agentWorkflowName,
			Task:            conversation.Child.Task, WorkingDirectory: deps.executor.Workspace(),
			Context: encoded,
		})
	require.NoError(t, err)
	_, err = fixture.base.store.PauseExecution(t.Context(), deps.sessionID,
		execution.ID, "leave child work paused")
	require.NoError(t, err)
	require.NoError(t, fixture.base.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{
		Directory: fixture.base.stateDirectory,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)
	runtime, err := NewRuntime(t.Context(), RuntimeOptions{
		Store: store, Resolver: fixture.base.runtime.resolver,
		Models: fixture.base.runtime.models, RootAgent: runtimeTestAgentName,
		DefaultModel: runtimeTestModelReference, DefaultWorkspace: fixture.base.workspace,
		ConfigDirectory:     fixture.base.configDirectory,
		ModelRequestTimeout: runtimeTestTurnTimeout,
		MaxContextTokens:    runtimeTestMaxContextTokens,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, runtime.Shutdown(context.WithoutCancel(t.Context())))
	})
	saved, err := store.GetExecution(t.Context(), deps.sessionID, execution.ID)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(saved.ContextJSON), &conversation))
	coldDeps := *deps
	coldDeps.executor, coldDeps.runtime = nil, nil
	restoredDeps, restored, err := runtime.restoreChildWorkflowScope(t.Context(), &coldDeps,
		run, conversation)
	require.NoError(t, err)
	assert.Same(t, runtime, restoredDeps.runtime)
	assert.Equal(t, prepared.maximumRounds, restored.maximumRounds)
	after, err := store.GetExecution(t.Context(), deps.sessionID, execution.ID)
	require.NoError(t, err)
	assert.Equal(t, saved.State, after.State)
	assert.Equal(t, saved.EventCount, after.EventCount)
}

func TestChildRestorationKeepsCompactorAfterDefaultsChange(t *testing.T) {
	t.Parallel()
	fixture, deps, launch, run, _ := initialChildConversationFixture(t)
	fixture.base.runtime.compactionMode = config.CompactionModeSummarize
	prepared, err := fixture.base.runtime.prepareChildConversation(t.Context(),
		deps, launch, "inspect child files", run)
	require.NoError(t, err)
	conversation, err := fixture.base.runtime.childWorkflowConversation(
		t.Context(), deps, launch, run, prepared)
	require.NoError(t, err)
	fixture.base.runtime.compactionMode = config.CompactionModeDropOldest
	fixture.base.runtime.agentLimits.MaxChildTurns = 1
	fixture.base.runtime.compactionOptions.ModelReference = "unknown/model"
	restoredDeps, restored, err := fixture.base.runtime.restoreChildWorkflowScope(
		t.Context(), deps, run, conversation)
	require.NoError(t, err)
	require.NotNil(t, restored.compactor)
	assert.Equal(t, conversation.Compaction.Plan, restored.compactor.plan)
	assert.Equal(t, conversation.Compaction.ModelReference,
		restored.compactor.options.ModelReference)
	assert.Equal(t, prepared.maximumRounds, restored.maximumRounds)
	assert.Equal(t, prepared.remainingRounds, restored.remainingRounds)
	assert.Same(t, fixture.base.store, restored.compactor.options.Store)
	assert.Equal(t, restoredDeps.executor.Workspace(),
		restored.compactor.options.Workspace)
}

func TestChildRestorationRejectsMalformedOrForeignState(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		change  func(*workflowConversation)
		wantErr error
	}{
		{
			name: "runtime", wantErr: commerr.ErrRequiredFieldNotSet,
			change: func(c *workflowConversation) { c.Runtime = nil },
		},
		{
			name: "bootstrap", wantErr: commerr.ErrRequiredFieldNotSet,
			change: func(c *workflowConversation) { c.Child = nil },
		},
		{
			name: "foreign run", wantErr: commerr.ErrConflict,
			change: func(c *workflowConversation) { c.Child.AgentRunID = uuid.New() },
		},
		{
			name: "foreign session", wantErr: commerr.ErrConflict,
			change: func(c *workflowConversation) { c.Child.SessionID = uuid.New() },
		},
		{
			name: "directory", wantErr: commerr.ErrConflict,
			change: func(c *workflowConversation) {
				c.Runtime.Directory.Directory = "/another-example"
			},
		},
		{
			name: "skills", wantErr: commerr.ErrValidationFailed,
			change: func(c *workflowConversation) {
				c.Runtime.Skills.CompactedThrough = -1
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, deps, launch, run, prepared := initialChildConversationFixture(t)
			conversation, err := fixture.base.runtime.childWorkflowConversation(
				t.Context(), deps, launch, run, prepared)
			require.NoError(t, err)
			tc.change(&conversation)
			_, _, err = fixture.base.runtime.restoreChildWorkflowScope(t.Context(),
				deps, run, conversation)
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}
