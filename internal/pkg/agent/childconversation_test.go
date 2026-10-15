package agent

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func initialChildConversationFixture(
	t *testing.T,
	turns ...elelemtest.Turn,
) (
	executionLoopFixture, *launchAgentDeps, preparedChildLaunch,
	*AgentRun, *preparedChildConversation,
) {
	t.Helper()
	fixture, root := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver(turns...))
	root.skillLedger.record("parent-only")
	deps := fixture.base.runtime.launchAgentDeps(root)
	fork, err := deps.executor.Fork(t.Context())
	require.NoError(t, err)
	deps.executor = fork
	input := launchAgentInput{
		Task: "inspect child files",
		AgentDefinition: &agentDefinitionInput{
			Name: "exampleChild", Instructions: "read the child sources",
		},
	}
	launch, err := fixture.base.runtime.prepareChildLaunch(t.Context(), deps,
		input)
	require.NoError(t, err)
	launch.skillLedger.record("child-only")
	run := &AgentRun{
		ID: uuid.New(), SessionID: deps.sessionID,
		ParentTurnID: deps.parentTurnID, RequestID: deps.requestID,
		Name: launch.definition.name, Depth: launch.depth,
		Definition: launch.definition.kind,
	}
	require.NoError(t, fixture.base.runtime.createChildAgentRun(t.Context(),
		deps, input, launch, run))
	_, err = fixture.base.store.AppendAgentRunMessages(t.Context(),
		deps.sessionID, run.ID, []session.AgentRunMessageInput{{
			Role: models.MessageRoleUser, Content: input.Task,
		}})
	require.NoError(t, err)
	prepared, err := fixture.base.runtime.prepareChildConversation(t.Context(),
		deps, launch, input.Task, run)
	require.NoError(t, err)

	return fixture, deps, launch, run, prepared
}

func TestInitialChildConversationRetainsIndependentRuntime(t *testing.T) {
	t.Parallel()
	fixture, deps, launch, run, prepared := initialChildConversationFixture(t)
	conversation, err := fixture.base.runtime.childWorkflowConversation(
		t.Context(), deps, launch, run, prepared)
	require.NoError(t, err)
	require.NotNil(t, conversation.Child)
	assert.Nil(t, conversation.Root)
	require.NotNil(t, conversation.Runtime)
	assert.Contains(t, conversation.Runtime.Skills.Delivered, "child-only")
	assert.NotContains(t, conversation.Runtime.Skills.Delivered, "parent-only")
	assert.Equal(t, run.ID, conversation.Child.AgentRunID)
	assert.Equal(t, prepared.maximumRounds, conversation.Child.MaxRounds)
	assert.Equal(t, launch.systemPrompt, conversation.SystemPrompt)
	assert.Equal(t, "inspect child files", conversation.Messages[0].Text())
	snapshot, err := harness.RestoreCheckpoint(conversation.Runtime.Harness)
	require.NoError(t, err)
	assert.Equal(t, deps.executor.Workspace(), snapshot.Workspace())
	assert.NotEmpty(t, conversation.Tools)
	assert.NotEmpty(t, conversation.Runtime.Directory.ContextHash)
	assert.NotEmpty(t, conversation.Runtime.Directory.PromptHash)
	assert.Empty(t, conversation.Runtime.Observations)
	writeRuntimeFile(t, filepath.Join(deps.executor.Workspace(), "child.txt"),
		"child source")
	_, err = deps.executor.ReadFile(t.Context(), tools.ReadFileInput{
		Path: "child.txt",
	})
	require.NoError(t, err)
	assert.Empty(t, conversation.Runtime.Observations)
	refreshed, err := fixture.base.runtime.childWorkflowConversation(
		t.Context(), deps, launch, run, prepared)
	require.NoError(t, err)
	require.Len(t, refreshed.Runtime.Observations, 1)
}

type childConversationModelResolver struct {
	model ModelClient
}

func (r childConversationModelResolver) ResolveModel(string) (ModelClient, error) {
	return r.model, nil
}

func TestChildConversationUsesOriginalModelAfterCatalogChange(t *testing.T) {
	t.Parallel()
	fixture, deps, launch, run, prepared := initialChildConversationFixture(t,
		elelemtest.Text("child reply"))
	conversation, err := fixture.base.runtime.childWorkflowConversation(
		t.Context(), deps, launch, run, prepared)
	require.NoError(t, err)
	original := deps.model.Model
	deps.model.Model.ID = "another-model"
	deps.model.Model.ContextSize = 1
	assert.Equal(t, original, *conversation.Child.Model)
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	request, saved, err := workflowModelRequest(childConversationModelResolver{
		model: deps.model,
	}, workflowRoundInput{Execution: workflowExecutionInput{
		SessionID: deps.sessionID, MaxRounds: prepared.remainingRounds,
		Context: encoded,
	}})
	require.NoError(t, err)
	response, err := runWorkflowModelResponse(t.Context(), request, saved)
	require.NoError(t, err)
	assert.Equal(t, "child reply", response.Text)
	assert.Equal(t, original.ID, response.Model)
}

func TestChildConversationRetainsOwnCompactionPlan(t *testing.T) {
	t.Parallel()
	fixture, deps, launch, run, _ := initialChildConversationFixture(t)
	fixture.base.runtime.compactionMode = config.CompactionModeSummarize
	prepared, err := fixture.base.runtime.prepareChildConversation(t.Context(),
		deps, launch, "inspect child files", run)
	require.NoError(t, err)
	conversation, err := fixture.base.runtime.childWorkflowConversation(
		t.Context(), deps, launch, run, prepared)
	require.NoError(t, err)
	require.NotNil(t, conversation.Compaction)
	require.Len(t, conversation.Compaction.Plan.Units, 1)
	rows, err := fixture.base.store.AgentRunHistory(t.Context(), deps.sessionID,
		run.ID)
	require.NoError(t, err)
	assert.Equal(t, rows.Messages[0].ID,
		conversation.Compaction.Plan.Units[0].FromMessageID)
	conversation.Compaction.Plan.Units[0].Roles[0] = elelem.RoleAssistant
	assert.Equal(t, elelem.RoleUser, prepared.compactor.plan.Units[0].Roles[0])
}

func TestChildConversationRejectsChangedLaunchAndBudget(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		change func(*preparedChildLaunch, *preparedChildConversation)
	}{
		{name: "instructions", change: func(
			launch *preparedChildLaunch, _ *preparedChildConversation,
		) {
			launch.definition.instructions = "another child"
		}},
		{name: "tools", change: func(
			launch *preparedChildLaunch, _ *preparedChildConversation,
		) {
			launch.definition.allowedTools = []string{toolNameWriteFile}
		}},
		{name: "budget", change: func(
			_ *preparedChildLaunch, prepared *preparedChildConversation,
		) {
			prepared.remainingRounds++
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, deps, launch, run, prepared := initialChildConversationFixture(t)
			tc.change(&launch, prepared)
			_, err := fixture.base.runtime.childWorkflowConversation(t.Context(),
				deps, launch, run, prepared)
			require.ErrorIs(t, err, commerr.ErrConflict)
		})
	}
}

func TestChildConversationRejectsMissingDependencies(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		deps     *launchAgentDeps
		run      *AgentRun
		prepared *preparedChildConversation
	}{
		{
			name: "dependencies", run: &AgentRun{},
			prepared: &preparedChildConversation{scope: &directoryScope{}},
		},
		{
			name: "run", deps: &launchAgentDeps{},
			prepared: &preparedChildConversation{scope: &directoryScope{}},
		},
		{name: "prepared", deps: &launchAgentDeps{}, run: &AgentRun{}},
		{
			name: "scope", deps: &launchAgentDeps{}, run: &AgentRun{},
			prepared: &preparedChildConversation{},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var runtime Runtime
			_, err := runtime.childWorkflowConversation(t.Context(), tc.deps,
				preparedChildLaunch{}, tc.run, tc.prepared)
			require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
		})
	}
}

func TestWorkflowToolSnapshotsDetachSchemasAndKeepEmptyShape(t *testing.T) {
	t.Parallel()
	tools := elelem.NewToolSet()
	assert.Nil(t, snapshotWorkflowToolDefinitions(tools))
	schema := json.RawMessage(`{"type":"object"}`)
	tools.Add(elelem.Tool{
		Name: rootWorkflowProbeTool, ArgumentsSchema: schema,
		StrictArguments: true,
	})
	saved := snapshotWorkflowToolDefinitions(tools)
	require.Len(t, saved, 1)
	schema[0] = ' '
	assert.Equal(t, byte('{'), saved[0].ArgumentsSchema[0])
	assert.True(t, saved[0].StrictArguments)
	assert.Equal(t, rootWorkflowProbeTool, saved[0].Name)
}

func TestChildConversationRetainsUnrestrictedAndEmptyToolChoices(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		allowed []string
		canRead bool
	}{
		{name: "unrestricted", canRead: true},
		{name: "empty", allowed: []string{}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, deps, launch, run, _ := initialChildConversationFixture(t)
			launch.definition.allowedTools = tc.allowed
			stored, err := fixture.base.store.GetAgentRun(t.Context(),
				deps.sessionID, run.ID)
			require.NoError(t, err)

			prepared, err := fixture.base.runtime.prepareChildConversation(
				t.Context(), deps, launch, stored.Task, run)
			require.NoError(t, err)
			conversation, err := fixture.base.runtime.childWorkflowConversation(
				t.Context(), deps, launch, run, prepared)
			require.NoError(t, err)
			assert.Equal(t, tc.allowed, conversation.Child.AllowedTools)
			_, restored, err := fixture.base.runtime.restoreChildWorkflowScope(
				t.Context(), deps, run, conversation)
			require.NoError(t, err)
			_, canRead := restored.scope.context().tools.Get(toolNameReadFile)
			assert.Equal(t, tc.canRead, canRead)
		})
	}
}
