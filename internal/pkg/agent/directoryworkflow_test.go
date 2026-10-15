package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDirectoryWorkflowPendingHopSurvivesActivityReopen(t *testing.T) {
	t.Parallel()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	scope, err := fixture.base.runtime.rootDirectoryScope(t.Context(), prepared)
	require.NoError(t, err)
	target := filepath.Join(prepared.workspace, "target")
	require.NoError(t, os.Mkdir(target, runtimeTestDirectoryMode))
	instructions := filepath.Join(target, "AGENTS.md")
	require.NoError(t, os.WriteFile(instructions,
		[]byte("Keep the original target instructions."), runtimeTestFileMode))
	conversation, err := fixture.base.runtime.rootWorkflowConversation(prepared,
		scope)
	require.NoError(t, err)
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	input := toolPhaseTestInput(fixture)
	input.Tool.Round.Execution.Context = encoded
	input.Tool.Call.Name = toolNameChangeDirectory
	input.Tool.Call.Arguments, err = json.Marshal(changeDirectoryInput{
		Directory: target,
	})
	require.NoError(t, err)
	input.Phase = workflowToolPhaseHandler
	input.State.LastPhase = workflowToolPhasePre
	fixture.engine.options.ToolPhase = func(ctx context.Context,
		input workflowToolPhaseInput,
	) (workflowToolPhaseState, error) {
		event := &elelem.ToolEvent{
			CallID: input.Tool.Call.ID,
			Tool:   elelem.Tool{Name: input.Tool.Call.Name},
		}
		if err := scope.bindWorkflowTool(ctx, event); err != nil {
			return input.State, ctxerrors.Wrap(err, "bind hop fixture handler")
		}
		defer scope.finishTool(event.CallID)

		result, err := scope.handleTool(ctx, elelem.ToolInput{
			CallID: event.CallID, Name: input.Tool.Call.Name,
			Arguments: input.Tool.Call.Arguments,
		})
		if err != nil {
			return input.State, ctxerrors.Wrap(err, "run hop fixture handler")
		}

		scope.pending.skills.record("retainedSkill")
		scope.pending.hooks.add("retainedCall", []string{"retained injection"})

		pending, err := scope.workflowPendingDirectory()
		if err != nil {
			return input.State, ctxerrors.Wrap(err, "save hop fixture candidate")
		}

		return workflowToolPhaseState{
			Pending: pending, Outcome: workflowToolResult{Result: result},
		}, nil
	}
	result, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	require.NotNil(t, result.Output.Pending)
	assert.Nil(t, scope.pending)
	assert.Equal(t, prepared.workspace, scope.context().executor.Workspace())
	fixture.stop()
	require.NoError(t, fixture.base.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{
		Directory: fixture.base.stateDirectory,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)
	fixture.engine.options.Store = store
	fixture.engine.options.GenerationID = uuid.New()
	fixture.engine.options.Lifecycle = t.Context()
	fixture.base.runtime.store = store
	prepared.turn.store = store
	saved, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, saved.Decision)
	require.NoError(t, os.WriteFile(instructions,
		[]byte("New instructions must not replace the pending checkpoint."),
		runtimeTestFileMode))
	_, restored, err := fixture.base.runtime.restoreRootWorkflowScope(
		t.Context(), prepared, conversation)
	require.NoError(t, err)
	require.NoError(t, restored.restoreWorkflowPendingDirectory(t.Context(),
		saved.Output.Pending))
	assert.Equal(t, prepared.workspace, restored.context().executor.Workspace())
	event := &elelem.ToolEvent{
		CallID: input.Tool.Call.ID,
		Tool:   elelem.Tool{Name: toolNameChangeDirectory},
		Result: &saved.Output.Outcome.Result, Phase: elelem.ToolPhasePostRun,
	}
	require.NoError(t, restored.bindWorkflowTool(t.Context(), event))
	require.NoError(t, restored.toolHook(t.Context(), event))
	_, err = restored.toolInjection(t.Context(), event)
	require.NoError(t, err)
	assert.Equal(t, target, restored.context().executor.Workspace())
	assert.Equal(t, saved.Output.Pending.Prompt, restored.context().prompt)
	assert.Contains(t, restored.context().prompt,
		"Keep the original target instructions.")
	assert.NotContains(t, restored.context().prompt, "New instructions")
	assert.Contains(t, restored.context().skills.held(), "retainedSkill")
	injection, err := restored.context().hooks.messageInjection(t.Context(),
		&elelem.ToolEvent{CallID: "retainedCall"})
	require.NoError(t, err)
	require.NotNil(t, injection)
	assert.Equal(t, "retained injection", injection.Content)
	assert.Empty(t, restored.calls)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	t.Cleanup(cancel)
	release, err := restored.gate.acquire(ctx, true)
	require.NoError(t, err)
	release()
}

func TestDirectoryWorkflowMissingPendingStateClearsCandidate(t *testing.T) {
	t.Parallel()
	scope := &directoryScope{pending: &directoryContext{prompt: "previous"}}
	require.NoError(t, scope.restoreWorkflowPendingDirectory(t.Context(), nil))
	assert.Nil(t, scope.pending)
	saved, err := scope.workflowPendingDirectory()
	require.NoError(t, err)
	assert.Nil(t, saved)
}

func TestDirectoryWorkflowInvalidPendingStatePreservesCandidate(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		input   *workflowPendingDirectory
		cancel  bool
		wantErr error
	}{
		{
			name: "missing runtime", input: &workflowPendingDirectory{Prompt: "saved"},
			wantErr: commerr.ErrRequiredFieldNotSet,
		},
		{name: "missing prompt", input: &workflowPendingDirectory{
			Runtime: &workflowRuntimeState{},
		}, wantErr: commerr.ErrRequiredFieldNotSet},
		{name: "cancelled replacement", cancel: true, wantErr: context.Canceled},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			previous := &directoryContext{prompt: "previous"}
			scope := &directoryScope{pending: previous}
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			if tc.cancel {
				cancel()
			}

			err := scope.restoreWorkflowPendingDirectory(ctx, tc.input)
			require.ErrorIs(t, err, tc.wantErr)
			assert.Same(t, previous, scope.pending)
		})
	}
}

func TestDirectoryWorkflowDamagedPendingContextPreservesCandidate(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		change  func(*workflowPendingDirectory)
		wantErr error
	}{
		{name: "changed directory", change: func(saved *workflowPendingDirectory) {
			saved.Runtime.Directory.Directory = filepath.Dir(
				saved.Runtime.Directory.Directory)
		}, wantErr: commerr.ErrConflict},
		{name: "missing harness", change: func(saved *workflowPendingDirectory) {
			saved.Runtime.Harness = json.RawMessage(`{}`)
		}, wantErr: commerr.ErrValidationFailed},
		{name: "invalid skill", change: func(saved *workflowPendingDirectory) {
			saved.Runtime.Skills = &workflowSkillState{Delivered: []string{" "}}
		}, wantErr: commerr.ErrValidationFailed},
		{name: "invalid hook", change: func(saved *workflowPendingDirectory) {
			saved.Runtime.Hooks = &workflowHookState{
				Messages: map[string][]string{"": {"message"}},
			}
		}, wantErr: commerr.ErrValidationFailed},
		{name: "invalid observation", change: func(saved *workflowPendingDirectory) {
			saved.Runtime.Observations = []tools.ObservedPath{{Path: "relative"}}
		}, wantErr: commerr.ErrValidationFailed},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, prepared := newRootModelWorkflowFixture(t,
				elelemtest.NewScriptedDriver())
			scope, err := fixture.base.runtime.rootDirectoryScope(t.Context(),
				prepared)
			require.NoError(t, err)
			target := filepath.Join(prepared.workspace, "target")
			require.NoError(t, os.Mkdir(target, runtimeTestDirectoryMode))
			_, err = scope.change(t.Context(), changeDirectoryInput{
				Directory: target,
			})
			require.NoError(t, err)
			previous := scope.pending
			saved, err := scope.workflowPendingDirectory()
			require.NoError(t, err)
			tc.change(saved)
			err = scope.restoreWorkflowPendingDirectory(t.Context(), saved)
			require.ErrorIs(t, err, tc.wantErr)
			assert.Same(t, previous, scope.pending)
		})
	}
}
