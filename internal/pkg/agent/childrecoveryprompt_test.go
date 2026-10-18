package agent

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChildRecoveryKeepsTheSpentRoundBudget(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		spent int
	}{
		{name: "current limit", spent: defaultMaxChildAgentTurns},
		{name: "original limit before a configuration increase", spent: 1},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, run, driver := recoveryFixture(t)
			ctx := context.Background()
			segment, err := fixture.store.CreateModelRun(ctx, run.SessionID, session.CreateModelRunInput{
				TurnID: run.ParentTurnID, AgentRunID: &run.ID, Stage: models.ModelRunStageChild,
				ModelReference: runtimeTestModelReference, ConnectionName: "test", RequestedModelID: runtimeTestModelID, RequestSettingsJSON: fmt.Sprintf(`{"maxRounds":%d}`, tc.spent),
			})
			require.NoError(t, err)
			for round := range tc.spent {
				_, err := fixture.store.CreateModelCall(ctx, run.SessionID, segment.ID, session.CreateModelCallInput{Round: int64(round), RequestMessagesJSON: `[]`, RequestToolsJSON: `[]`})
				require.NoError(t, err)
			}
			_ = restartRecoveryFixture(t, fixture)
			require.Eventually(t, func() bool {
				stored, err := fixture.store.GetAgentRun(ctx, run.SessionID, run.ID)

				return err == nil && stored.State == models.AgentRunStateFailed
			}, workflowTestResultTimeout, time.Millisecond)
			assert.Empty(t, driver.Requests())
		})
	}
}

func TestChildRecoveryUsesSummaryAndRawTail(t *testing.T) {
	t.Parallel()
	fixture, run, driver := recoveryFixture(t)
	ctx := context.Background()
	messages, err := fixture.store.AppendAgentRunMessages(ctx, run.SessionID, run.ID, []session.AgentRunMessageInput{
		{Role: models.MessageRoleUser, Content: "original task"},
		{Role: models.MessageRoleAssistant, Content: "old progress"},
		{Role: models.MessageRoleUser, Content: "recent correction"},
	})
	require.NoError(t, err)
	_, err = fixture.store.CreateAgentRunCompaction(ctx, run.SessionID, run.ID, session.AgentRunCompactionInput{
		FromMessageID: messages[0].ID, ToMessageID: messages[1].ID,
		FromSequence: 1, ToSequence: 2, DirectFromSequence: 1, DirectToSequence: 2,
		Summary: "saved summary", SourceMessageCount: 2,
		InputTokenCount: 2, SummaryTokenCount: 1,
		ModelID: runtimeTestModelID, PromptHash: "test",
	})
	require.NoError(t, err)
	_ = restartRecoveryFixture(t, fixture)
	awaitRecoveredChild(t, fixture, run)
	requests := driver.Requests()
	require.Len(t, requests, 1)
	require.Len(t, requests[0].Messages, 3)
	assert.Contains(t, requests[0].Messages[1].Text(), "saved summary")
	assert.Equal(t, "recent correction", requests[0].Messages[2].Text())
}

func TestChildRecoveryAllowsOnlyTheRemainingRound(t *testing.T) {
	t.Parallel()
	fixture, run, driver := recoveryFixture(t)
	ctx := context.Background()
	segment, err := fixture.store.CreateModelRun(ctx, run.SessionID, session.CreateModelRunInput{
		TurnID: run.ParentTurnID, AgentRunID: &run.ID, Stage: models.ModelRunStageChild,
		ModelReference: runtimeTestModelReference, ConnectionName: "test", RequestedModelID: runtimeTestModelID,
		RequestSettingsJSON: `{"maxRounds":2}`,
	})
	require.NoError(t, err)
	_, err = fixture.store.CreateModelCall(ctx, run.SessionID, segment.ID, session.CreateModelCallInput{
		Round: 0, RequestMessagesJSON: `[]`, RequestToolsJSON: `[]`,
	})
	require.NoError(t, err)
	_ = restartRecoveryFixture(t, fixture)
	awaitRecoveredChild(t, fixture, run)
	assert.Len(t, driver.Requests(), 1)
	stage := models.ModelRunStageChild
	segments, err := fixture.store.ListModelRuns(ctx, run.SessionID, session.ListModelRunsOptions{Stage: &stage})
	require.NoError(t, err)
	require.Len(t, segments.Items, 2)
	for _, stored := range segments.Items {
		if stored.ID == segment.ID {
			continue
		}
		calls, err := fixture.store.ListModelCalls(ctx, run.SessionID, stored.ID, session.ListModelCallsOptions{})
		require.NoError(t, err)
		require.Len(t, calls.Items, 1)
		assert.Equal(t, int64(1), calls.Items[0].Round)
	}
}
