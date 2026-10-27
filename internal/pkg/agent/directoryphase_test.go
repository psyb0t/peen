package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runRestoredDirectoryPhases(
	t *testing.T,
	fixture executionLoopFixture,
	prepared *preparedTurn,
	input workflowToolPhaseInput,
	afterHandler func(),
) (workflowToolPhaseState, *directoryScope) {
	t.Helper()
	scope, err := fixture.base.runtime.rootDirectoryScope(t.Context(), prepared)
	require.NoError(t, err)
	conversation, err := fixture.base.runtime.rootWorkflowConversation(prepared,
		scope)
	require.NoError(t, err)
	input.Tool.Round.Execution.Context, err = json.Marshal(conversation)
	require.NoError(t, err)
	for _, phase := range []workflowToolPhase{
		workflowToolPhasePre, workflowToolPhaseHandler,
		workflowToolPhaseOutcome, workflowToolPhasePost, workflowToolPhaseProject,
	} {
		input.Phase = phase
		require.NoError(t, json.Unmarshal(input.Tool.Round.Execution.Context,
			&conversation))
		_, scope, err = fixture.base.runtime.restoreRootWorkflowScope(t.Context(),
			prepared, conversation)
		require.NoError(t, err)
		output, err := runDirectoryWorkflowToolPhase(t.Context(), scope, input,
			workflowToolOptions{})
		require.NoError(t, err)
		assert.Empty(t, scope.calls)
		encoded, err := json.Marshal(output)
		require.NoError(t, err)
		var saved workflowToolPhaseState
		require.NoError(t, json.Unmarshal(encoded, &saved))
		input.State = saved
		advanceWorkflowToolPhase(&input.Tool, &input.State)
		if phase == workflowToolPhaseHandler {
			afterHandler()
		}
	}

	return input.State, scope
}

func TestDirectoryPhaseReadRetainsResultAndProofThroughFreshScopes(t *testing.T) {
	t.Parallel()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	path := filepath.Join(prepared.workspace, runtimeToolFileName)
	require.NoError(t, os.WriteFile(path, []byte(runtimeToolFileBody),
		runtimeTestFileMode))
	input := toolPhaseTestInput(fixture)
	input.Tool.Call.Arguments = json.RawMessage(runtimeToolArguments(t,
		runtimeToolFileName))
	state, scope := runRestoredDirectoryPhases(t, fixture, prepared, input,
		func() {
			require.NoError(t, os.WriteFile(path, []byte("changed after read"),
				runtimeTestFileMode))
		})
	assert.False(t, state.Outcome.Result.IsError)
	assert.Contains(t, state.Outcome.Result.Content, "alpha")
	_, err := scope.context().executor.EditFile(t.Context(), tools.EditFileInput{
		Path: path, Edits: []tools.TextEdit{{Old: "changed", New: "modified"}},
	})
	require.ErrorIs(t, err, tools.ErrStaleHash)
}

func TestDirectoryPhaseHopRetainsFrozenTargetThroughFreshScopes(t *testing.T) {
	t.Parallel()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	target := filepath.Join(prepared.workspace, "target")
	require.NoError(t, os.Mkdir(target, runtimeTestDirectoryMode))
	path := filepath.Join(target, "AGENTS.md")
	require.NoError(t, os.WriteFile(path, []byte("Retain this target context."),
		runtimeTestFileMode))
	input := toolPhaseTestInput(fixture)
	input.Tool.Call.Name = toolNameChangeDirectory
	arguments, err := json.Marshal(changeDirectoryInput{Directory: target})
	require.NoError(t, err)
	input.Tool.Call.Arguments = arguments
	state, scope := runRestoredDirectoryPhases(t, fixture, prepared, input,
		func() {
			require.NoError(t, os.WriteFile(path, []byte("Changed instructions."),
				runtimeTestFileMode))
		})
	assert.False(t, state.Outcome.Result.IsError)
	assert.Nil(t, state.Pending)
	assert.Equal(t, target, scope.context().executor.Workspace())
	assert.Contains(t, scope.context().prompt, "Retain this target context.")
	assert.NotContains(t, scope.context().prompt, "Changed instructions.")
}
