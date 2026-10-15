package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func childModelFixture(
	t *testing.T,
	turns ...elelemtest.Turn,
) (executionLoopFixture, workflowRoundInput, *agentRunSink) {
	t.Helper()
	fixture, deps, launch, run, prepared := initialChildConversationFixture(t,
		turns...)
	run.events = newAgentRunEventBuffer(defaultMaxAgentRunEventCount,
		defaultMaxAgentRunEventBytes)
	conversation, err := fixture.base.runtime.childWorkflowConversation(
		t.Context(), deps, launch, run, prepared)
	require.NoError(t, err)
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	execution, err := fixture.base.store.CreateExecution(t.Context(),
		deps.sessionID, session.CreateExecutionInput{
			ID: uuid.New(), RootTurnID: deps.parentTurnID,
			ParentExecutionID: &fixture.input.ExecutionID, AgentRunID: &run.ID,
			WorkflowVersion:  agentWorkflowName,
			Task:             conversation.Child.Task,
			WorkingDirectory: deps.executor.Workspace(), Context: encoded,
		})
	require.NoError(t, err)
	input := workflowRoundInput{Execution: workflowExecutionInput{
		SessionID: deps.sessionID, ExecutionID: execution.ID,
		MaxRounds: prepared.remainingRounds, Context: encoded,
	}}
	sink := newAgentRunSink(fixture.base.store, deps.sessionID, run, nil,
		conversation.ModelReference)
	fixture.engine.options.ModelRound = func(
		ctx context.Context, round workflowRoundInput,
	) (workflowRoundResult, error) {
		return fixture.base.runtime.workflowChildModelRound(ctx, sink, round)
	}

	return fixture, input, sink
}

func TestChildWorkflowModelAuditsAndReusesCompletedResult(t *testing.T) {
	t.Parallel()
	fixture, input, sink := childModelFixture(t,
		elelemtest.Text("child completed response"))
	result, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, "child completed response", result.Output.Text)
	reused, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	runs, err := fixture.base.store.ListModelRuns(t.Context(), sink.sessionID,
		session.ListModelRunsOptions{})
	require.NoError(t, err)
	require.Len(t, runs.Items, 1)
	assert.Equal(t, models.ModelRunStageChild, runs.Items[0].Stage)
	assert.Equal(t, sink.run.ID, *runs.Items[0].AgentRunID)
	var settings rootWorkflowModelAuditSettings
	require.NoError(t, json.Unmarshal(
		[]byte(runs.Items[0].RequestSettingsJSON), &settings))
	assert.Equal(t, input.Execution.ExecutionID, settings.ExecutionID)
	assert.False(t, settings.AutoToolCalls)
	assert.NotEqual(t, uuid.Nil, settings.ActivityID)
	history, err := fixture.base.store.AgentRunHistory(t.Context(), sink.sessionID,
		sink.run.ID)
	require.NoError(t, err)
	require.Len(t, history.Messages, 2)
	assert.Equal(t, result.Output.Text, history.Messages[1].Content)
}

func TestChildWorkflowModelRetriesInterruptedProviderWithAnotherAttempt(
	t *testing.T,
) {
	t.Parallel()
	fixture, input, sink := childModelFixture(t,
		elelemtest.Turn{Err: context.Canceled}, elelemtest.Text("resumed child"))
	_, err := fixture.engine.modelRound(t.Context(), input)
	require.ErrorIs(t, err, context.Canceled)
	fixture.engine.options.GenerationID = uuid.New()
	result, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, "resumed child", result.Output.Text)
	runs, err := fixture.base.store.ListModelRuns(t.Context(), sink.sessionID,
		session.ListModelRunsOptions{})
	require.NoError(t, err)
	require.Len(t, runs.Items, 2)
	var first, second rootWorkflowModelAuditSettings
	require.NoError(t, json.Unmarshal(
		[]byte(runs.Items[0].RequestSettingsJSON), &first))
	require.NoError(t, json.Unmarshal(
		[]byte(runs.Items[1].RequestSettingsJSON), &second))
	assert.Equal(t, first.ActivityID, second.ActivityID)
	assert.Equal(t, second.Attempt+1, first.Attempt)
	history, err := fixture.base.store.AgentRunHistory(t.Context(), sink.sessionID,
		sink.run.ID)
	require.NoError(t, err)
	assert.Len(t, history.Messages, 2)
}

func TestChildWorkflowModelRefusesForeignSinkBeforeProvider(t *testing.T) {
	t.Parallel()
	fixture, input, sink := childModelFixture(t,
		elelemtest.Text("must not run"))
	sink.run.ID = uuid.New()
	_, err := fixture.engine.modelRound(t.Context(), input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	runs, err := fixture.base.store.ListModelRuns(t.Context(), sink.sessionID,
		session.ListModelRunsOptions{})
	require.NoError(t, err)
	assert.Empty(t, runs.Items)
}

func TestChildWorkflowModelReturnsToolsForSeparateActivities(t *testing.T) {
	t.Parallel()
	fixture, input, sink := childModelFixture(t,
		elelemtest.ToolCall("childRead", toolNameReadFile,
			`{"path":"not-read-in-model.txt"}`))
	result, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	require.Len(t, result.Output.Calls, 1)
	assert.Equal(t, toolNameReadFile, result.Output.Calls[0].Name)
	history, err := fixture.base.store.AgentRunHistory(t.Context(), sink.sessionID,
		sink.run.ID)
	require.NoError(t, err)
	require.Len(t, history.Messages, 2)
	assert.Equal(t, models.MessageRoleAssistant, history.Messages[1].Role)
	assert.Contains(t, history.Messages[1].ToolCallsJSON, "childRead")
}

func TestChildWorkflowModelContinuesForArrivalDuringProvider(t *testing.T) {
	t.Parallel()
	fixture, input, sink := childModelFixture(t,
		elelemtest.Text("response while another message arrives"))
	accepted := false
	sink.liveSink = func(event Event) error {
		if accepted || event.Type != EventTypeAgentRunTextDelta {
			return nil
		}

		accepted = true
		_, err := fixture.base.store.AcceptChildExecutionMessage(t.Context(),
			input.Execution.SessionID, input.Execution.ExecutionID,
			session.AcceptExecutionMessageInput{
				RequestID: uuid.New(), WorkingDirectory: fixture.base.workspace,
				Message: "arrived during provider", Capacity: 1,
			})

		return err
	}
	result, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	assert.True(t, accepted)
	assert.True(t, result.Output.Continue)
	page, err := fixture.base.store.ListExecutionMessages(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID,
		session.ListExecutionInboxOptions{State: models.InboxMessageStateQueued})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "arrived during provider", page.Items[0].Message)
}
