package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cschleiden/go-workflows/client"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateRetainedRootAgentRejectsInvalidMetadata(t *testing.T) {
	t.Parallel()
	saved := &workflowRootBootstrap{RootAgent: runtimeTestAgentName}
	testCases := []struct {
		name    string
		root    *workflowRootBootstrap
		wantErr error
	}{
		{name: "missing", wantErr: commerr.ErrValidationFailed},
		{name: "version", root: &workflowRootBootstrap{
			Version:   rootWorkflowBootstrapVersion + 1,
			RootAgent: runtimeTestAgentName,
		}, wantErr: commerr.ErrValidationFailed},
		{name: "name", root: &workflowRootBootstrap{
			Version: rootWorkflowBootstrapVersion,
		}, wantErr: commerr.ErrValidationFailed},
		{name: "changed", root: &workflowRootBootstrap{
			Version:   rootWorkflowBootstrapVersion,
			RootAgent: replacementRootAgentName,
		}, wantErr: commerr.ErrConflict},
		{name: "original", root: &workflowRootBootstrap{
			Version:   rootWorkflowBootstrapVersion,
			RootAgent: runtimeTestAgentName,
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			encoded, err := json.Marshal(struct {
				Root *workflowRootBootstrap `json:"root"`
			}{Root: tc.root})
			require.NoError(t, err)
			err = validateRetainedRootAgent(saved, &models.Execution{
				ContextJSON: string(encoded),
			})
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
	t.Run("malformed", func(t *testing.T) {
		t.Parallel()
		err := validateRetainedRootAgent(saved, &models.Execution{
			ContextJSON: "invalid JSON",
		})
		var syntax *json.SyntaxError
		require.ErrorAs(t, err, &syntax)
	})
}

const (
	replacementRootAgentName     = "replacement"
	replacementRootAgentDocument = `---
name: %s
description: replacement root
allowed-tools: %s
---
Replacement root instructions.`
)

func TestRootFactoryRetainsOriginalAgentAfterDefaultChanges(t *testing.T) {
	t.Parallel()
	fixture, original, input := rootFactoryFixture(t,
		elelemtest.Text("continued retained work"))
	fixture.base.store.ReleaseTurn(original.lease)
	writeRuntimeFile(t, filepath.Join(fixture.base.configDirectory,
		".agents", "agents", replacementRootAgentName+".md"),
		fmt.Sprintf(replacementRootAgentDocument, replacementRootAgentName,
			toolNameReadFile))
	runtime, err := NewRuntime(t.Context(), RuntimeOptions{
		Store: fixture.base.store, Resolver: fixture.base.runtime.resolver,
		Models: fixture.base.runtime.models, RootAgent: replacementRootAgentName,
		DefaultModel:     runtimeTestModelReference,
		DefaultWorkspace: fixture.base.workspace,
		ConfigDirectory:  fixture.base.configDirectory,
		MaxContextTokens: runtimeTestMaxContextTokens,
		TurnTimeout:      runtimeTestTurnTimeout,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, runtime.Shutdown(context.WithoutCancel(t.Context())))
	})
	restored, scope, err := runtime.restoreRootWorkflowTurn(t.Context(), input,
		session.RestoreExecutionTurnInput{OwnerGenerationID: uuid.New()}, nil)
	require.NoError(t, err)
	assert.Equal(t, original.turn.requestID, restored.turn.requestID)
	assert.Equal(t, original.prompt.SystemMessage(), scope.context().prompt)
	_, hasCommand := scope.context().tools.Get(toolNameRunCommand)
	assert.True(t, hasCommand)
	assert.Equal(t, replacementRootAgentName, runtime.rootAgent)
	assertRetainedRootDirectoryIdentity(t, runtime, scope,
		filepath.Join(fixture.base.workspace, "other-directory"))
	var completed atomic.Int32
	callbacks := newRootWorkflowCallbacks(runtime,
		fixture.engine.options.GenerationID, func(event Event) error {
			if event.Type == EventTypeTurnCompleted {
				completed.Add(1)
			}

			return nil
		})
	callbacks.bind(&fixture.engine.options)
	instance := fixture.start(t)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "continued retained work", result.Text)
	assert.EqualValues(t, 1, completed.Load())
	fixture.stop()
}

func assertRetainedRootDirectoryIdentity(
	t *testing.T,
	runtime *Runtime,
	scope *directoryScope,
	directory string,
) {
	t.Helper()
	writeRuntimeFile(t, filepath.Join(directory, "AGENTS.md"),
		"Rules for the target directory.")
	marker, err := scope.change(t.Context(), changeDirectoryInput{
		Directory: directory,
	})
	require.NoError(t, err)
	assert.Equal(t, directory, marker.Directory)
	definition, err := scope.pending.snapshot.Agent(runtimeTestAgentName)
	require.NoError(t, err)
	assert.Contains(t, scope.pending.prompt, definition.Instructions)
	_, hasCommand := scope.pending.tools.Get(toolNameRunCommand)
	assert.True(t, hasCommand)
	opened, err := runtime.store.OpenWorkspace(t.Context(), directory,
		session.OpenSessionOptions{RootAgent: replacementRootAgentName})
	require.NoError(t, err)
	prepared, err := runtime.prepareTurn(t.Context(), TurnRequest{
		SessionID: &opened.Session.ID, RequestID: uuid.New(),
		Message: "future work", Workspace: directory,
	})
	require.NoError(t, err)
	t.Cleanup(func() { runtime.store.ReleaseTurn(prepared.lease) })
	replacement, err := runtime.rootDirectoryScope(t.Context(), prepared)
	require.NoError(t, err)
	_, hasCommand = replacement.context().tools.Get(toolNameRunCommand)
	assert.False(t, hasCommand)
}

func rootFactoryFixture(t *testing.T, scripted ...elelemtest.Turn) (
	executionLoopFixture, *preparedTurn, workflowExecutionInput,
) {
	t.Helper()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver(scripted...))
	scope, err := fixture.base.runtime.rootDirectoryScope(t.Context(), prepared)
	require.NoError(t, err)
	result, err := fixture.base.runtime.prepareRootWorkflowRound(t.Context(),
		prepared, scope, workflowRoundInput{
			Execution: fixture.input,
		})
	require.NoError(t, err)
	input := fixture.input
	input.Context = result

	return fixture, prepared, input
}

func TestRootFactoryIdentifiesBusyAdmissionWithoutTakingTheOtherTurn(t *testing.T) {
	t.Parallel()
	fixture, prepared, input := rootFactoryFixture(t)
	fixture.base.store.ReleaseTurn(prepared.lease)
	other, err := fixture.base.store.AcquireTurn(t.Context(), input.SessionID, session.StartTurnInput{
		RequestID: uuid.New(), Workspace: fixture.base.workspace,
	})
	require.NoError(t, err)
	t.Cleanup(func() { fixture.base.store.ReleaseTurn(other) })
	_, _, err = fixture.base.runtime.restoreRootWorkflowTurn(t.Context(), input,
		session.RestoreExecutionTurnInput{OwnerGenerationID: uuid.New()}, nil)
	require.ErrorIs(t, err, session.ErrSessionBusy)
	require.ErrorIs(t, err, errRootAdmissionBusy)
	require.NoError(t, fixture.base.store.AppendCheckpoint(t.Context(), other,
		[]session.MessageInput{{Role: models.MessageRoleUser, Content: "the active turn still owns its lease"}}, nil))
}

func TestRootFactoryRejectsMalformedCompactionBeforeClaim(t *testing.T) {
	t.Parallel()
	fixture, original, input := rootFactoryFixture(t)
	fixture.base.store.ReleaseTurn(original.lease)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(input.Context, &conversation))
	conversation.Compaction = &workflowCompactionState{}
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	input.Context = encoded
	_, _, err = fixture.base.runtime.restoreRootWorkflowTurn(t.Context(),
		input, session.RestoreExecutionTurnInput{OwnerGenerationID: uuid.New()},
		nil)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	assert.False(t, fixture.base.store.IsActive(input.SessionID))
	_, err = fixture.base.store.GetExecutionTurnLease(t.Context(),
		input.SessionID, input.ExecutionID)
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

func TestRootFactoryRestoresCompactionAfterDatabaseReopen(t *testing.T) {
	t.Parallel()
	scenario := setupCompactionScenario(t, 2)
	base := scenario.fixture
	prepared, err := base.runtime.prepareTurn(t.Context(), TurnRequest{
		RequestID: uuid.New(), Workspace: base.workspace,
		Message: "continue example work",
	})
	require.NoError(t, err)
	scope, err := base.runtime.rootDirectoryScope(t.Context(), prepared)
	require.NoError(t, err)
	conversation, err := base.runtime.rootWorkflowConversation(prepared, scope)
	require.NoError(t, err)
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	execution, err := base.store.CreateExecution(t.Context(),
		prepared.lease.SessionID, session.CreateExecutionInput{
			ID: uuid.New(), RootTurnID: prepared.lease.TurnID,
			WorkflowVersion: agentWorkflowName,
			Task:            prepared.input.Message, WorkingDirectory: prepared.workspace,
			Context: encoded,
		})
	require.NoError(t, err)
	base.store.ReleaseTurn(prepared.lease)
	require.NoError(t, base.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: base.stateDirectory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)
	_, err = store.RecoverInterrupted(t.Context())
	require.NoError(t, err)
	runtime, err := NewRuntime(t.Context(), RuntimeOptions{
		Store: store, Resolver: base.runtime.resolver, Models: base.runtime.models,
		RootAgent: runtimeTestAgentName, DefaultModel: runtimeTestModelReference,
		DefaultWorkspace: base.workspace, ConfigDirectory: base.configDirectory,
		MaxContextTokens: runtimeTestMaxContextTokens,
		CompactionMode:   config.CompactionModeDropOldest,
		TurnTimeout:      runtimeTestTurnTimeout,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, runtime.Shutdown(context.WithoutCancel(t.Context())))
	})
	saved, err := store.GetExecution(t.Context(), execution.SessionID,
		execution.ID)
	require.NoError(t, err)
	input := workflowExecutionInput{
		SessionID:   saved.SessionID,
		ExecutionID: saved.ID, MaxRounds: 4, MaxConcurrentTools: 2,
		Context: json.RawMessage(saved.ContextJSON),
	}
	restored, _, err := runtime.restoreRootWorkflowTurn(t.Context(), input,
		session.RestoreExecutionTurnInput{OwnerGenerationID: uuid.New()}, nil)
	require.NoError(t, err)
	require.NotNil(t, restored.compactor)
	assert.Equal(t, conversation.Compaction.Plan, restored.compactor.plan)
	assert.Equal(t, conversation.Compaction.ModelReference,
		restored.compactor.options.ModelReference)
	assert.Equal(t, conversation.Compaction.Prompt,
		restored.compactor.options.Prompt)
	assert.Equal(t, conversation.Compaction.PromptHash,
		restored.compactor.options.PromptHash)
	assert.Equal(t, conversation.Compaction.MaxOutputTokens,
		restored.compactor.options.MaxOutputTokens)
	assert.Equal(t, conversation.Compaction.Timeout,
		restored.compactor.options.Timeout)
	assert.Same(t, store, restored.compactor.options.Store)
	assert.Equal(t, prepared.lease.TurnID, restored.lease.TurnID)
}

func TestRootFactoryRestoresFromDurableInputWithoutPreparedTurn(t *testing.T) {
	t.Parallel()
	fixture, original, input := rootFactoryFixture(t)
	fixture.base.store.ReleaseTurn(original.lease)
	before, err := fixture.base.store.ListEvents(t.Context(), input.SessionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	holder := session.RestoreExecutionTurnInput{OwnerGenerationID: uuid.New()}
	restored, scope, err := fixture.base.runtime.restoreRootWorkflowTurn(
		t.Context(), input, holder, nil)
	require.NoError(t, err)
	assert.Equal(t, original.lease.TurnID, restored.lease.TurnID)
	assert.NotEqual(t, original.lease.ID, restored.lease.ID)
	assert.Equal(t, original.turn.requestID, restored.turn.requestID)
	assert.Equal(t, original.input.Message, restored.input.Message)
	assert.Equal(t, original.prompt.SystemMessage(), scope.context().prompt)
	require.NoError(t, fixture.base.store.AppendCheckpoint(t.Context(),
		restored.lease, nil, nil))
	err = fixture.base.store.AppendCheckpoint(t.Context(), original.lease,
		nil, nil)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	turns, err := fixture.base.store.ListTurns(t.Context(), input.SessionID,
		session.ListTurnsOptions{})
	require.NoError(t, err)
	require.Len(t, turns.Items, 1)
	after, err := fixture.base.store.ListEvents(t.Context(), input.SessionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	assert.Equal(t, before.Items, after.Items)
	repeated, _, err := fixture.base.runtime.restoreRootWorkflowTurn(
		t.Context(), input, holder, nil)
	require.NoError(t, err)
	assert.Equal(t, restored.lease, repeated.lease)
}

func TestRootFactoryRejectsAlteredBootstrapBeforeClaim(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		change func(*workflowRootBootstrap)
	}{
		{name: "request", change: func(saved *workflowRootBootstrap) {
			saved.RequestID = uuid.New()
		}},
		{name: "session", change: func(saved *workflowRootBootstrap) {
			saved.SessionID = uuid.New()
		}},
		{name: "turn", change: func(saved *workflowRootBootstrap) {
			saved.TurnID = uuid.New()
		}},
		{name: "message", change: func(saved *workflowRootBootstrap) {
			saved.Message = "a different accepted task"
		}},
		{name: "root agent", change: func(saved *workflowRootBootstrap) {
			saved.RootAgent = replacementRootAgentName
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, original, input := rootFactoryFixture(t)
			fixture.base.store.ReleaseTurn(original.lease)
			var conversation workflowConversation
			require.NoError(t, json.Unmarshal(input.Context, &conversation))
			tc.change(conversation.Root)
			encoded, err := json.Marshal(conversation)
			require.NoError(t, err)
			input.Context = encoded
			_, _, err = fixture.base.runtime.restoreRootWorkflowTurn(
				t.Context(), input, session.RestoreExecutionTurnInput{
					OwnerGenerationID: uuid.New(),
				}, nil)
			require.ErrorIs(t, err, commerr.ErrConflict)
			assert.False(t, fixture.base.store.IsActive(input.SessionID))
		})
	}
}

func TestRootFactoryRestoresInFreshRuntimeAfterDatabaseReopen(t *testing.T) {
	t.Parallel()
	fixture, original, input := rootFactoryFixture(t)
	require.NoError(t, original.userMessages.enqueue(t.Context(), TurnRequest{
		RequestID: uuid.New(), Message: "pending before database reopen",
	}))
	require.NoError(t, fixture.base.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{
		Directory: fixture.base.stateDirectory,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)
	_, err = store.RecoverInterrupted(t.Context())
	require.NoError(t, err)
	writeRuntimeFile(t, filepath.Join(fixture.base.configDirectory,
		".agents", "agents", replacementRootAgentName+".md"),
		fmt.Sprintf(replacementRootAgentDocument, replacementRootAgentName,
			toolNameReadFile))
	runtime, err := NewRuntime(t.Context(), RuntimeOptions{
		Store: store, Resolver: fixture.base.runtime.resolver,
		Models: fixture.base.runtime.models, RootAgent: replacementRootAgentName,
		DefaultModel:     runtimeTestModelReference,
		DefaultWorkspace: fixture.base.workspace,
		ConfigDirectory:  fixture.base.configDirectory,
		MaxContextTokens: runtimeTestMaxContextTokens,
		TurnTimeout:      runtimeTestTurnTimeout,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, runtime.Shutdown(context.WithoutCancel(t.Context())))
	})
	execution, err := store.GetExecution(t.Context(), input.SessionID,
		input.ExecutionID)
	require.NoError(t, err)
	input.Context = json.RawMessage(execution.ContextJSON)
	require.NoError(t, os.WriteFile(filepath.Join(fixture.base.workspace,
		"AGENTS.md"), []byte("Changed after interruption."), runtimeTestFileMode))
	restored, scope, err := runtime.restoreRootWorkflowTurn(t.Context(), input,
		session.RestoreExecutionTurnInput{OwnerGenerationID: uuid.New()}, nil)
	require.NoError(t, err)
	assert.Equal(t, original.lease.TurnID, restored.lease.TurnID)
	assert.Equal(t, original.turn.requestID, restored.turn.requestID)
	assert.Equal(t, original.prompt.SystemMessage(), scope.context().prompt)
	assert.NotContains(t, scope.context().prompt, "Changed after interruption.")
	assert.Equal(t, replacementRootAgentName, runtime.rootAgent)
	count, _ := restored.userMessages.arrival()
	assert.Equal(t, 1, count)
	assert.Same(t, restored.userMessages, scope.options.queue)
	require.NoError(t, store.AppendCheckpoint(t.Context(), restored.lease,
		nil, nil))
}

func TestRootFactoryCannotRestorePausedWork(t *testing.T) {
	t.Parallel()
	fixture, original, input := rootFactoryFixture(t)
	fixture.base.store.ReleaseTurn(original.lease)
	_, err := fixture.base.store.PauseExecution(t.Context(), input.SessionID,
		input.ExecutionID, "retain paused work")
	require.NoError(t, err)
	_, _, err = fixture.base.runtime.restoreRootWorkflowTurn(t.Context(), input,
		session.RestoreExecutionTurnInput{OwnerGenerationID: uuid.New()}, nil)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	assert.False(t, fixture.base.store.IsActive(input.SessionID))
}

func TestRootFactoryUsesReplacementEventSink(t *testing.T) {
	t.Parallel()
	fixture, original, input := rootFactoryFixture(t)
	fixture.base.store.ReleaseTurn(original.lease)
	var received Event
	sink := func(event Event) error {
		received = event

		return nil
	}
	restored, _, err := fixture.base.runtime.restoreRootWorkflowTurn(t.Context(),
		input, session.RestoreExecutionTurnInput{OwnerGenerationID: uuid.New()},
		sink)
	require.NoError(t, err)
	require.NoError(t, restored.turn.emit(t.Context(), EventTypeSkillInjected,
		skillInjectedPayload{Name: "example", Content: "saved skill"}))
	assert.Equal(t, EventTypeSkillInjected, received.Type)
}

func TestRootFactoryRejectsInvalidContextBeforeClaim(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		change  func(*workflowConversation)
		wantErr error
	}{
		{
			name: "missing bootstrap", wantErr: commerr.ErrValidationFailed,
			change: func(saved *workflowConversation) { saved.Root = nil },
		},
		{
			name: "unknown version", wantErr: commerr.ErrValidationFailed,
			change: func(saved *workflowConversation) { saved.Root.Version++ },
		},
		{
			name: "missing root", wantErr: commerr.ErrValidationFailed,
			change: func(saved *workflowConversation) {
				saved.Root.RootAgent = ""
			},
		},
		{
			name: "missing runtime", wantErr: commerr.ErrValidationFailed,
			change: func(saved *workflowConversation) { saved.Runtime = nil },
		},
		{
			name: "missing prompt", wantErr: commerr.ErrValidationFailed,
			change: func(saved *workflowConversation) { saved.SystemPrompt = "" },
		},
		{
			name: "unavailable model", wantErr: ErrModelUnavailable,
			change: func(saved *workflowConversation) {
				saved.ModelReference = "unknown/model"
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, original, input := rootFactoryFixture(t)
			fixture.base.store.ReleaseTurn(original.lease)
			var conversation workflowConversation
			require.NoError(t, json.Unmarshal(input.Context, &conversation))
			tc.change(&conversation)
			encoded, err := json.Marshal(conversation)
			require.NoError(t, err)
			input.Context = encoded
			_, _, err = fixture.base.runtime.restoreRootWorkflowTurn(
				t.Context(), input, session.RestoreExecutionTurnInput{
					OwnerGenerationID: uuid.New(),
				}, nil)
			require.ErrorIs(t, err, tc.wantErr)
			assert.False(t, fixture.base.store.IsActive(input.SessionID))
		})
	}
}
