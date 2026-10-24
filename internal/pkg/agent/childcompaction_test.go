package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nativeChildCompactionFixture(
	t *testing.T,
	summaryTurns ...elelemtest.Turn,
) (
	executionLoopFixture, *models.AgentRun, *elelemtest.ScriptedDriver,
) {
	t.Helper()
	fixture, prepared, driver := nativeRootCompactionFixture(t, summaryTurns...)
	run, err := fixture.base.store.CreateAgentRun(t.Context(),
		fixture.input.SessionID, session.StartAgentRunInput{
			ParentTurnID: prepared.lease.TurnID, RequestID: uuid.New(),
			Name: "exampleChild", Definition: models.AgentRunDefinitionAdHoc,
			Depth: 1, Workspace: prepared.workspace,
			ModelReference: runtimeTestModelReference, ModelID: runtimeTestModelID,
			Task: "summarize child history", Instructions: "inspect example files",
			AllowedToolsJSON: `[]`, SystemPrompt: "child prompt",
		})
	require.NoError(t, err)
	messages := make([]session.AgentRunMessageInput, 0, 6)
	for range 3 {
		messages = append(messages,
			session.AgentRunMessageInput{
				Role:    models.MessageRoleUser,
				Content: "child request",
			},
			session.AgentRunMessageInput{
				Role:    models.MessageRoleAssistant,
				Content: "child answer",
			})
	}

	stored, err := fixture.base.store.AppendAgentRunMessages(t.Context(),
		fixture.input.SessionID, run.ID, messages)
	require.NoError(t, err)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(fixture.input.Context, &conversation))
	conversation.Root = nil
	conversation.Child, err = snapshotChildWorkflowBootstrap(run,
		fixture.input.MaxRounds, 0)
	require.NoError(t, err)
	conversation.SystemPrompt = run.SystemPrompt
	conversation.Messages, _, err = convertRecoveredHistory(stored)
	require.NoError(t, err)
	conversation.Compaction.Plan, err = buildChildReconstructionPlan(1,
		false, stored)
	require.NoError(t, err)
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	child, err := fixture.base.store.CreateExecution(t.Context(), run.SessionID,
		session.CreateExecutionInput{
			ID: uuid.New(), RootTurnID: run.ParentTurnID,
			ParentExecutionID: &fixture.input.ExecutionID, AgentRunID: &run.ID,
			WorkflowVersion: agentWorkflowName, Task: run.Task,
			WorkingDirectory: run.Workspace, Context: encoded,
		})
	require.NoError(t, err)
	fixture.input.ExecutionID = child.ID
	fixture.input.Context = encoded
	fixture.engine.options.CompactRound = fixture.base.runtime.workflowChildCompactRound

	return fixture, run, driver
}

func TestNativeChildCompactionCommitsAndReusesSavedSummary(t *testing.T) {
	t.Parallel()
	fixture, child, driver := nativeChildCompactionFixture(t)
	input := workflowRoundInput{Execution: fixture.input}
	result, err := fixture.engine.compactRound(t.Context(), input)
	require.NoError(t, err)
	require.NotNil(t, result.Output.Compaction)
	assert.Equal(t, 4, driver.Calls())
	head, err := fixture.base.store.LatestAgentRunCompaction(t.Context(),
		fixture.input.SessionID, child.ID)
	require.NoError(t, err)
	require.NotNil(t, head)
	assert.Equal(t, result.Output.Compaction.ID, head.ID)
	requests := driver.Requests()
	assert.Contains(t, requests[3].Messages[1].Text(), "child request")
	assert.NotContains(t, requests[3].Messages[1].Text(), "first answer")
	assert.Empty(t, requests[3].Tools)
	var saved workflowConversation
	require.NoError(t, json.Unmarshal(result.Output.Context, &saved))
	require.NoError(t, validateWorkflowCompactionConversation(saved))
	assert.Equal(t, compactionSummaryLead+"retained summary",
		saved.Messages[0].Text())
	repeated, err := fixture.engine.compactRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, repeated.Decision)
	assert.Equal(t, 4, driver.Calls())
	runs, err := fixture.base.store.ListModelRuns(t.Context(),
		fixture.input.SessionID, session.ListModelRunsOptions{})
	require.NoError(t, err)
	var audited *models.ModelRun
	for _, run := range runs.Items {
		if run.Stage == models.ModelRunStageCompaction {
			audited = run
		}
	}

	require.NotNil(t, audited)
	assert.Equal(t, &child.ID, audited.AgentRunID)
	var settings rootWorkflowModelAuditSettings
	require.NoError(t, json.Unmarshal([]byte(audited.RequestSettingsJSON),
		&settings))
	assert.Equal(t, fixture.input.ExecutionID, settings.ExecutionID)
	assert.Equal(t, head.ID, settings.ActivityID)
}

func TestNativeChildCompactionFailurePreservesRawHistory(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		turn    elelemtest.Turn
		wantErr error
	}{
		{
			name: "empty", turn: elelemtest.Text(" "),
			wantErr: ErrCompactionEmptySummary,
		},
		{
			name: "provider", turn: elelemtest.Turn{Err: commerr.ErrFetchFailed},
			wantErr: commerr.ErrFetchFailed,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, child, _ := nativeChildCompactionFixture(t, tc.turn)
			_, err := fixture.engine.compactRound(t.Context(),
				workflowRoundInput{Execution: fixture.input})
			require.ErrorIs(t, err, tc.wantErr)
			history, err := fixture.base.store.AgentRunHistory(t.Context(),
				fixture.input.SessionID, child.ID)
			require.NoError(t, err)
			assert.Nil(t, history.Compaction)
			assert.Len(t, history.Messages, 6)
		})
	}
}

func TestNativeChildCompactionRefusesForeignSourceBeforeProvider(t *testing.T) {
	t.Parallel()
	fixture, _, driver := nativeChildCompactionFixture(t)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(fixture.input.Context, &conversation))
	conversation.Compaction.Plan.Units[0].FromMessageID = uuid.New()
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	fixture.input.Context = encoded
	_, err = fixture.engine.compactRound(t.Context(),
		workflowRoundInput{Execution: fixture.input})
	require.ErrorIs(t, err, commerr.ErrConflict)
	assert.Equal(t, 3, driver.Calls())
}

func TestNativeChildCompactionUnderBudgetDoesNotCallProvider(t *testing.T) {
	t.Parallel()
	fixture, _, driver := nativeChildCompactionFixture(t)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(fixture.input.Context, &conversation))
	conversation.MaxContextTokens = 1000
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	fixture.input.Context = encoded
	result, err := fixture.engine.compactRound(t.Context(),
		workflowRoundInput{Execution: fixture.input})
	require.NoError(t, err)
	assert.Nil(t, result.Output.Compaction)
	assert.Equal(t, json.RawMessage(encoded), result.Output.Context)
	assert.Equal(t, 3, driver.Calls())
}

func TestNativeChildCompactionWithNothingToSummarizeKeepsContext(
	t *testing.T,
) {
	t.Parallel()
	fixture, _, driver := nativeChildCompactionFixture(t)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(fixture.input.Context, &conversation))
	conversation.MaxContextTokens = 200
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	fixture.input.Context = encoded
	result, err := fixture.engine.compactRound(t.Context(),
		workflowRoundInput{Execution: fixture.input})
	require.NoError(t, err)
	assert.Nil(t, result.Output.Compaction)
	assert.Equal(t, json.RawMessage(encoded), result.Output.Context)
	assert.Equal(t, 3, driver.Calls())
}

func TestNativeChildCompactionRequiresLeaseBeforeProvider(t *testing.T) {
	t.Parallel()
	fixture, _, driver := nativeChildCompactionFixture(t)
	_, err := fixture.base.runtime.workflowChildCompactRound(t.Context(),
		workflowRoundInput{Execution: fixture.input})
	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
	assert.Equal(t, 3, driver.Calls())
	ctx := context.WithValue(t.Context(), workflowActivityLeaseContextKey{},
		session.ActivityLease{})
	_, err = fixture.base.runtime.workflowChildCompactRound(ctx,
		workflowRoundInput{Execution: fixture.input})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	assert.Equal(t, 3, driver.Calls())
}

func TestNativeChildCompactionRejectsOtherConversationBeforeProvider(
	t *testing.T,
) {
	t.Parallel()
	testCases := []struct {
		name   string
		change func(*workflowConversation)
	}{
		{name: "root", change: func(value *workflowConversation) {
			value.Root = &workflowRootBootstrap{}
		}},
		{name: "model", change: func(value *workflowConversation) {
			value.ModelReference = "unknown/model"
		}},
		{name: "prompt", change: func(value *workflowConversation) {
			value.SystemPrompt = "another child prompt"
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, _, driver := nativeChildCompactionFixture(t)
			var conversation workflowConversation
			require.NoError(t, json.Unmarshal(fixture.input.Context, &conversation))
			tc.change(&conversation)
			encoded, err := json.Marshal(conversation)
			require.NoError(t, err)
			fixture.input.Context = encoded
			_, err = fixture.engine.compactRound(t.Context(),
				workflowRoundInput{Execution: fixture.input})
			require.ErrorIs(t, err, commerr.ErrConflict)
			assert.Equal(t, 3, driver.Calls())
		})
	}
}

func TestNativeChildCompactionUsesSavedSettingsAfterDefaultsChange(
	t *testing.T,
) {
	t.Parallel()
	fixture, _, driver := nativeChildCompactionFixture(t)
	fixture.base.runtime.compactionMode = config.CompactionModeDropOldest
	fixture.base.runtime.compactionOptions.ModelReference = "unknown/model"
	fixture.base.runtime.compactionOptions.MaxOutputTokens = 1000
	result, err := fixture.engine.compactRound(t.Context(),
		workflowRoundInput{Execution: fixture.input})
	require.NoError(t, err)
	require.NotNil(t, result.Output.Compaction)
	assert.Equal(t, runtimeTestModelID, result.Output.Compaction.ModelID)
	assert.Equal(t, 4, driver.Calls())
}
