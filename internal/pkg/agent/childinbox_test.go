package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func childWorkflowInboxFixture(
	t *testing.T,
) (executionLoopFixture, workflowRoundInput, workflowConversation, *skillLedger) {
	t.Helper()
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
			WorkflowVersion:  agentWorkflowName,
			Task:             conversation.Child.Task,
			WorkingDirectory: deps.executor.Workspace(), Context: encoded,
		})
	require.NoError(t, err)
	accepted := session.AcceptExecutionMessageInput{
		RequestID: uuid.New(), WorkingDirectory: deps.executor.Workspace(),
		Message: "continue child inspection", Capacity: 2,
		Parts: []session.ExecutionInboxPart{{
			SkillName: "childInboxSkill", Content: "child skill instructions",
		}},
	}
	_, err = fixture.base.store.AcceptChildExecutionMessage(t.Context(),
		deps.sessionID, execution.ID, accepted)
	require.NoError(t, err)
	input := workflowRoundInput{Execution: workflowExecutionInput{
		SessionID: deps.sessionID, ExecutionID: execution.ID,
		MaxRounds: prepared.remainingRounds, Context: encoded,
	}}
	ledger := prepared.scope.context().skills

	return fixture, input, conversation, ledger
}

func TestChildWorkflowInboxPreparesIndependentSavedContext(t *testing.T) {
	t.Parallel()
	fixture, input, conversation, ledger := childWorkflowInboxFixture(t)
	require.NoError(t, fixture.base.runtime.prepareChildWorkflowInbox(
		t.Context(), input, &conversation, ledger))
	require.Len(t, conversation.InboxIDs, 1)
	require.Len(t, conversation.Messages, 3)
	assert.Equal(t, "continue child inspection", conversation.Messages[1].Text())
	assert.Equal(t, "child skill instructions", conversation.Messages[2].Text())
	assert.Contains(t, ledger.held(), "childInboxSkill")
	assert.NotContains(t, ledger.held(), "parent-only")
	history, err := fixture.base.store.AgentRunHistory(t.Context(),
		input.Execution.SessionID, conversation.Child.AgentRunID)
	require.NoError(t, err)
	assert.Len(t, history.Messages, 1)
}

func TestChildWorkflowInboxDeliveryReusesOnlySavedRequestIDs(t *testing.T) {
	t.Parallel()
	fixture, input, conversation, ledger := childWorkflowInboxFixture(t)
	runtime := fixture.base.runtime
	require.NoError(t, runtime.prepareChildWorkflowInbox(t.Context(), input,
		&conversation, ledger))
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	input.Execution.Context = encoded
	saved, err := json.Marshal(input)
	require.NoError(t, err)
	claim, err := fixture.base.store.BeginExecutionActivity(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID, uuid.New(),
		session.ScheduleActivityInput{
			StepID: "model:0", Name: modelActivityName,
			Kind:         models.ActivityKindModel,
			ReplayPolicy: models.ActivityReplayPolicyRetry, Input: saved,
		})
	require.NoError(t, err)
	ctx := context.WithValue(t.Context(), workflowActivityLeaseContextKey{},
		claim.Lease)
	require.NoError(t, runtime.deliverChildWorkflowInbox(ctx, input, conversation))
	require.NoError(t, runtime.deliverChildWorkflowInbox(ctx, input, conversation))
	history, err := fixture.base.store.AgentRunHistory(t.Context(),
		input.Execution.SessionID, conversation.Child.AgentRunID)
	require.NoError(t, err)
	assert.Len(t, history.Messages, 3)
	_, err = fixture.base.store.AcceptChildExecutionMessage(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID,
		session.AcceptExecutionMessageInput{
			RequestID: uuid.New(), WorkingDirectory: fixture.base.workspace,
			Message: "later child request", Capacity: 2,
		})
	require.NoError(t, err)
	changed := input
	changed.Round++
	err = runtime.deliverChildWorkflowInbox(ctx, changed, conversation)
	require.ErrorIs(t, err, commerr.ErrConflict)
	page, err := fixture.base.store.ListExecutionMessages(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID,
		session.ListExecutionInboxOptions{State: models.InboxMessageStateQueued})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "later child request", page.Items[0].Message)
}

func TestChildWorkflowInboxRejectsChangedActorAndBudget(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		change  func(*workflowRoundInput, *workflowConversation)
		wantErr error
	}{
		{
			name: "missing child", wantErr: commerr.ErrRequiredFieldNotSet,
			change: func(_ *workflowRoundInput, c *workflowConversation) {
				c.Child = nil
			},
		},
		{
			name: "foreign child", wantErr: commerr.ErrConflict,
			change: func(_ *workflowRoundInput, c *workflowConversation) {
				c.Child.AgentRunID = uuid.New()
			},
		},
		{
			name: "foreign session", wantErr: commerr.ErrPermissionDenied,
			change: func(i *workflowRoundInput, _ *workflowConversation) {
				i.Execution.SessionID = uuid.New()
			},
		},
		{
			name: "exhausted round", wantErr: commerr.ErrValidationFailed,
			change: func(i *workflowRoundInput, _ *workflowConversation) {
				i.Round = i.Execution.MaxRounds
			},
		},
		{
			name: "missing execution", wantErr: commerr.ErrNotFound,
			change: func(i *workflowRoundInput, _ *workflowConversation) {
				i.Execution.ExecutionID = uuid.New()
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, input, conversation, ledger := childWorkflowInboxFixture(t)
			tc.change(&input, &conversation)
			err := fixture.base.runtime.prepareChildWorkflowInbox(t.Context(),
				input, &conversation, ledger)
			require.ErrorIs(t, err, tc.wantErr)
			assert.Empty(t, conversation.InboxIDs)
			assert.NotContains(t, ledger.held(), "childInboxSkill")
		})
	}
}

func TestChildWorkflowInboxPreservesPausedStateAndRequiresLease(t *testing.T) {
	t.Parallel()
	fixture, input, conversation, ledger := childWorkflowInboxFixture(t)
	err := fixture.base.runtime.deliverChildWorkflowInbox(t.Context(), input,
		conversation)
	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
	_, err = fixture.base.store.PauseExecution(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID, "operator pause")
	require.NoError(t, err)
	err = fixture.base.runtime.prepareChildWorkflowInbox(t.Context(), input,
		&conversation, ledger)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	row, err := fixture.base.store.GetExecution(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, row.State)
	assert.Empty(t, conversation.InboxIDs)
}
