package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cschleiden/go-workflows/client"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nativeRootCompactionFixture(
	t *testing.T,
	summaryTurns ...elelemtest.Turn,
) (
	executionLoopFixture, *preparedTurn, *elelemtest.ScriptedDriver,
) {
	t.Helper()
	turns := make([]elelemtest.Turn, 0, 4+len(summaryTurns))
	turns = append(turns,
		elelemtest.Text("first answer"), elelemtest.Text("second answer"),
		elelemtest.Text("third answer"),
	)
	if len(summaryTurns) == 0 {
		summaryTurns = []elelemtest.Turn{elelemtest.Text("retained summary")}
	}

	driver := elelemtest.NewScriptedDriver(append(turns, summaryTurns...)...).
		WithTokenCounter(compactionTokenCounter{perMessage: 100})
	base := newStandardCompactionFixture(t, driver)
	runCompactionTurns(t, base, nil, 1, 3)
	prepared, err := base.runtime.prepareTurn(t.Context(), TurnRequest{
		RequestID: uuid.New(), Workspace: base.workspace,
		Message: "continue example work",
	})
	require.NoError(t, err)
	t.Cleanup(func() { base.store.ReleaseTurn(prepared.lease) })
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
	fixture := newExecutionLoopFixtureWithInput(t, base,
		workflowExecutionInput{
			SessionID:   execution.SessionID,
			ExecutionID: execution.ID, MaxRounds: 4, MaxConcurrentTools: 2,
			Context: encoded,
		}, false)
	fixture.engine.options.CompactRound = func(
		ctx context.Context, input workflowRoundInput,
	) (workflowCompactionResult, error) {
		return base.runtime.workflowRootCompactRound(ctx, prepared, input)
	}

	return fixture, prepared, driver
}

func TestNativeRootCompactionCallsProviderAndCommitsOnce(t *testing.T) {
	t.Parallel()
	fixture, prepared, driver := nativeRootCompactionFixture(t)
	input := workflowRoundInput{Execution: fixture.input}
	result, err := fixture.engine.compactRound(t.Context(), input)
	require.NoError(t, err)
	require.NotNil(t, result.Output.Compaction)
	assert.Equal(t, "retained summary", result.Output.Compaction.Summary)
	assert.EqualValues(t, 4, result.Output.Compaction.SourceMessageCount)
	assert.Equal(t, 4, driver.Calls())
	requests := driver.Requests()
	require.Len(t, requests, 4)
	summaryRequest := requests[3]
	assert.Empty(t, summaryRequest.Tools)
	assert.NotContains(t, summaryRequest.Messages[1].Text(), prepared.input.Message)
	assert.Contains(t, summaryRequest.Messages[1].Text(), "first answer")
	assert.Contains(t, summaryRequest.Messages[1].Text(), "second answer")
	assert.NotContains(t, summaryRequest.Messages[1].Text(), "third answer")
	head, err := fixture.base.store.LatestCompaction(t.Context(),
		fixture.input.SessionID)
	require.NoError(t, err)
	require.NotNil(t, head)
	assert.Equal(t, result.Output.Compaction.ID, head.ID)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(result.Output.Context, &conversation))
	require.NoError(t, validateWorkflowCompactionConversation(conversation))
	assert.Equal(t, compactionSummaryLead+"retained summary",
		conversation.Messages[0].Text())
	assert.EqualValues(t, 4, conversation.Runtime.Skills.CompactedThrough)
	reused, err := fixture.engine.compactRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	assert.Equal(t, 4, driver.Calls())
	runs, err := fixture.base.store.ListModelRuns(t.Context(),
		fixture.input.SessionID, session.ListModelRunsOptions{})
	require.NoError(t, err)
	var compacted *models.ModelRun
	for _, run := range runs.Items {
		if run.Stage == models.ModelRunStageCompaction {
			compacted = run
		}
	}
	require.NotNil(t, compacted)
	var settings rootWorkflowModelAuditSettings
	require.NoError(t, json.Unmarshal([]byte(compacted.RequestSettingsJSON),
		&settings))
	assert.Equal(t, fixture.input.ExecutionID, settings.ExecutionID)
	assert.Equal(t, head.ID, settings.ActivityID)
	assert.EqualValues(t, 1, settings.Attempt)
}

func TestNativeRootCompactionFailureLeavesHistoryRaw(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		turn    elelemtest.Turn
		wantErr error
	}{
		{
			name: "empty summary", turn: elelemtest.Text(" \n "),
			wantErr: ErrCompactionEmptySummary,
		},
		{
			name: "provider error", turn: elelemtest.Turn{Err: commerr.ErrFetchFailed},
			wantErr: commerr.ErrFetchFailed,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, _, driver := nativeRootCompactionFixture(t, tc.turn)
			_, err := fixture.engine.compactRound(t.Context(),
				workflowRoundInput{Execution: fixture.input})
			require.ErrorIs(t, err, tc.wantErr)
			assert.Equal(t, 4, driver.Calls())
			head, err := fixture.base.store.LatestCompaction(t.Context(),
				fixture.input.SessionID)
			require.NoError(t, err)
			assert.Nil(t, head)
			history, err := fixture.base.store.CompletedHistory(t.Context(),
				fixture.input.SessionID)
			require.NoError(t, err)
			assert.Len(t, history.Messages, 6)
			page, err := fixture.base.store.ListExecutionActivities(t.Context(),
				fixture.input.SessionID, fixture.input.ExecutionID,
				session.ListEventsOptions{})
			require.NoError(t, err)
			require.Len(t, page.Items, 1)
			assert.Equal(t, models.ActivityStateFailed, page.Items[0].State)
			assert.Nil(t, page.Items[0].OutputJSON)
		})
	}
}

func TestNativeRootCompactionSkipsUnderBudget(t *testing.T) {
	t.Parallel()
	fixture, _, driver := nativeRootCompactionFixture(t)
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
	assert.JSONEq(t, string(encoded), string(result.Output.Context))
	assert.Equal(t, 3, driver.Calls())
}

func TestNativeRootCompactionSDKReusesSavedSummary(t *testing.T) {
	t.Parallel()
	fixture, _, driver := nativeRootCompactionFixture(t,
		elelemtest.Text("retained summary"), elelemtest.Text("continued answer"))
	_, err := fixture.engine.compactRound(t.Context(),
		workflowRoundInput{Execution: fixture.input})
	require.NoError(t, err)
	assert.Equal(t, 4, driver.Calls())
	instance := startCompactingWorkflow(t, fixture)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, "continued answer", result.Text)
	assert.Equal(t, 5, driver.Calls())
	requests := driver.Requests()
	require.Len(t, requests, 5)
	assert.Equal(t, compactionSummaryLead+"retained summary",
		requests[4].Messages[1].Text())
	fixture.stop()
}

type nativeSummarySizeCounter struct{}

func (nativeSummarySizeCounter) Count(
	messages []elelem.Message,
	_ []elelem.Tool,
) (int, error) {
	count := len(messages) * 100
	for _, message := range messages {
		if strings.HasPrefix(message.Text(), compactionSummaryLead) &&
			len(message.Text()) > 100 {
			count += 1000
		}
	}

	return count, nil
}

func TestNativeRootCompactionRejectsOversizedSummary(t *testing.T) {
	t.Parallel()
	fixture, _, driver := nativeRootCompactionFixture(t,
		elelemtest.Text(strings.Repeat("x", 200)))
	driver.WithTokenCounter(nativeSummarySizeCounter{})
	_, err := fixture.engine.compactRound(t.Context(),
		workflowRoundInput{Execution: fixture.input})
	require.ErrorIs(t, err, ErrCompactionInsufficient)
	assert.Equal(t, 4, driver.Calls())
	head, err := fixture.base.store.LatestCompaction(t.Context(),
		fixture.input.SessionID)
	require.NoError(t, err)
	assert.Nil(t, head)
	history, err := fixture.base.store.CompletedHistory(t.Context(),
		fixture.input.SessionID)
	require.NoError(t, err)
	assert.Len(t, history.Messages, 6)
}

func TestNativeRootCompactionRequiresLeaseBeforeProvider(t *testing.T) {
	t.Parallel()
	fixture, prepared, driver := nativeRootCompactionFixture(t)
	_, err := fixture.base.runtime.workflowRootCompactRound(t.Context(),
		prepared, workflowRoundInput{Execution: fixture.input})
	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
	assert.Equal(t, 3, driver.Calls())
}
