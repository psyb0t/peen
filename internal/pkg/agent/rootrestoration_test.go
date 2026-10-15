package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootRestorationUsesFrozenContextWithoutOpeningAnotherTurn(t *testing.T) {
	t.Parallel()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	path := filepath.Join(prepared.workspace, "example.txt")
	require.NoError(t, os.WriteFile(path, []byte("original"),
		runtimeTestFileMode))
	_, err := prepared.executor.ReadFile(t.Context(), tools.ReadFileInput{
		Path: path,
	})
	require.NoError(t, err)
	prepared.skillLedger.record("core:planning")
	scope, err := fixture.base.runtime.rootDirectoryScope(t.Context(), prepared)
	require.NoError(t, err)
	encoded, err := fixture.base.runtime.prepareRootWorkflowRound(t.Context(),
		prepared, scope, workflowRoundInput{Execution: fixture.input})
	require.NoError(t, err)
	var saved workflowConversation
	require.NoError(t, json.Unmarshal(encoded, &saved))
	require.NoError(t, os.WriteFile(filepath.Join(prepared.workspace,
		"AGENTS.md"), []byte("Changed after checkpoint."), runtimeTestFileMode))
	require.NoError(t, os.WriteFile(path, []byte("changed"), runtimeTestFileMode))
	restored, resumed, err := fixture.base.runtime.restoreRootWorkflowScope(
		t.Context(), prepared, saved)
	require.NoError(t, err)
	assert.Equal(t, prepared.lease, restored.lease)
	assert.Equal(t, saved.SystemPrompt, resumed.context().prompt)
	assert.NotContains(t, resumed.context().prompt, "Changed after checkpoint.")
	assert.Contains(t, resumed.context().skills.held(), "core:planning")
	assert.NotSame(t, prepared.executor, restored.executor)
	_, err = restored.executor.EditFile(t.Context(), tools.EditFileInput{
		Path: path, Edits: []tools.TextEdit{{Old: "changed", New: "modified"}},
	})
	require.ErrorIs(t, err, tools.ErrStaleHash)
	turns, err := fixture.base.store.ListTurns(t.Context(),
		fixture.input.SessionID, session.ListTurnsOptions{})
	require.NoError(t, err)
	require.Len(t, turns.Items, 1)
	assert.Equal(t, prepared.lease.TurnID, turns.Items[0].ID)
}

func TestRootRestorationRejectsMissingState(t *testing.T) {
	t.Parallel()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	_, _, err := fixture.base.runtime.restoreRootWorkflowScope(t.Context(),
		prepared, workflowConversation{})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

func TestRootRestorationKeepsPendingHookState(t *testing.T) {
	t.Parallel()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	scope, err := fixture.base.runtime.rootDirectoryScope(t.Context(), prepared)
	require.NoError(t, err)
	scope.context().hooks.add(hookInjectTestCallID, []string{hookInjectTestNote})
	scope.context().hooks.deny(&elelem.ToolEvent{CallID: "deniedCall"},
		"retained hook denial")
	conversation, err := fixture.base.runtime.rootWorkflowConversation(prepared,
		scope)
	require.NoError(t, err)
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	var saved workflowConversation
	require.NoError(t, json.Unmarshal(encoded, &saved))
	_, restored, err := fixture.base.runtime.restoreRootWorkflowScope(
		t.Context(), prepared, saved)
	require.NoError(t, err)
	hooks := restored.context().hooks
	injection, err := hooks.messageInjection(t.Context(),
		&elelem.ToolEvent{CallID: hookInjectTestCallID})
	require.NoError(t, err)
	require.NotNil(t, injection)
	assert.Equal(t, hookInjectTestNote, injection.Content)
	denial, denied := hooks.takeDenial("deniedCall")
	assert.True(t, denied)
	assert.Equal(t, "retained hook denial", denial)
	_, denied = hooks.takeDenial("deniedCall")
	assert.False(t, denied)
}

func TestRootRestorationReadsHookStateFromPreparationCheckpoint(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver()
	fixture, prepared := newRootModelWorkflowFixture(t, driver)
	prepared.toolHooks.add(hookInjectTestCallID, []string{hookInjectTestNote})
	prepared.toolHooks.deny(&elelem.ToolEvent{CallID: "deniedCall"},
		"checkpointed hook denial")
	_, err := fixture.engine.prepareRound(t.Context(),
		workflowRoundInput{Execution: fixture.input})
	require.NoError(t, err)
	handle, err := db.Open(t.Context(), db.Config{
		Directory: fixture.base.stateDirectory,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)
	activities, err := store.ListExecutionActivities(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	require.Len(t, activities.Items, 1)
	var saved workflowConversation
	require.NoError(t, json.Unmarshal([]byte(activities.Items[0].ContextJSON),
		&saved))
	_, restored, err := fixture.base.runtime.restoreRootWorkflowScope(
		t.Context(), prepared, saved)
	require.NoError(t, err)
	injection, err := restored.context().hooks.messageInjection(t.Context(),
		&elelem.ToolEvent{CallID: hookInjectTestCallID})
	require.NoError(t, err)
	require.NotNil(t, injection)
	assert.Equal(t, hookInjectTestNote, injection.Content)
	denial, denied := restored.context().hooks.takeDenial("deniedCall")
	assert.True(t, denied)
	assert.Equal(t, "checkpointed hook denial", denial)
	assert.Empty(t, driver.Requests())
}

func TestRootRestorationRetainsCompactionAfterDefaultsChange(t *testing.T) {
	t.Parallel()
	scenario := setupCompactionScenario(t, 2)
	runtime := scenario.fixture.runtime
	prepared, err := runtime.prepareTurn(t.Context(), TurnRequest{
		RequestID: uuid.New(), Workspace: scenario.fixture.workspace,
		Message: "continue example work",
	})
	require.NoError(t, err)
	t.Cleanup(func() { scenario.fixture.store.ReleaseTurn(prepared.lease) })
	scope, err := runtime.rootDirectoryScope(t.Context(), prepared)
	require.NoError(t, err)
	conversation, err := runtime.rootWorkflowConversation(prepared, scope)
	require.NoError(t, err)
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	var saved workflowConversation
	require.NoError(t, json.Unmarshal(encoded, &saved))
	require.NotNil(t, saved.Compaction)
	runtime.compactionMode = config.CompactionModeDropOldest
	runtime.compactionOptions.Prompt = "changed summary instructions"
	runtime.compactionOptions.ModelReference = "changed/model"
	runtime.compactionOptions.MaxOutputTokens++
	seed := *prepared
	seed.compactor = nil
	restored, _, err := runtime.restoreRootWorkflowScope(t.Context(), &seed,
		saved)
	require.NoError(t, err)
	require.NotNil(t, restored.compactor)
	assert.Equal(t, saved.Compaction.Plan, restored.compactor.plan)
	assert.Equal(t, saved.Compaction.Prompt, restored.compactor.options.Prompt)
	assert.Equal(t, saved.Compaction.ModelReference,
		restored.compactor.options.ModelReference)
	assert.Equal(t, saved.Compaction.MaxOutputTokens,
		restored.compactor.options.MaxOutputTokens)
	assert.Equal(t, saved.Compaction.Timeout, restored.compactor.options.Timeout)
	assert.Equal(t, prepared.lease, restored.lease)
	assert.NotSame(t, prepared.compactor, restored.compactor)
}

func TestRootRestorationRejectsInvalidSavedState(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		change  func(*workflowConversation)
		wantErr error
	}{
		{
			name: "missing runtime", wantErr: commerr.ErrValidationFailed,
			change: func(saved *workflowConversation) { saved.Runtime = nil },
		},
		{
			name: "missing prompt", wantErr: commerr.ErrValidationFailed,
			change: func(saved *workflowConversation) { saved.SystemPrompt = "" },
		},
		{
			name: "malformed harness", wantErr: commerr.ErrValidationFailed,
			change: func(saved *workflowConversation) {
				saved.Runtime.Harness = json.RawMessage(`{}`)
			},
		},
		{
			name: "directory mismatch", wantErr: commerr.ErrConflict,
			change: func(saved *workflowConversation) {
				saved.Runtime.Directory.Directory = "/other"
			},
		},
		{
			name: "invalid observations", wantErr: commerr.ErrValidationFailed,
			change: func(saved *workflowConversation) {
				saved.Runtime.Observations = []tools.ObservedPath{{Path: "relative"}}
			},
		},
		{
			name: "invalid skills", wantErr: commerr.ErrValidationFailed,
			change: func(saved *workflowConversation) {
				saved.Runtime.Skills = &workflowSkillState{Delivered: []string{""}}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, prepared := newRootModelWorkflowFixture(t,
				elelemtest.NewScriptedDriver())
			scope, err := fixture.base.runtime.rootDirectoryScope(t.Context(),
				prepared)
			require.NoError(t, err)
			state, err := snapshotWorkflowRuntime(scope.context())
			require.NoError(t, err)
			saved := workflowConversation{
				Runtime: state, SystemPrompt: scope.context().prompt,
			}
			tc.change(&saved)
			_, _, err = fixture.base.runtime.restoreRootWorkflowScope(
				t.Context(), prepared, saved)
			require.ErrorIs(t, err, tc.wantErr)
			assert.Equal(t, fixture.base.workspace, prepared.workspace)
		})
	}
}
