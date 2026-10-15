package session

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func executionChildCompactionFixture(t *testing.T) (
	executionFixture, *models.Execution, *ActivityClaim, CompleteActivityInput,
) {
	t.Helper()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	messages := make([]*models.AgentRunMessage, 0, 3)
	for range 3 {
		messages = append(messages, appendChildMessage(t.Context(), t,
			fixture.store, fixture.sessionID, *child.AgentRunID,
			models.MessageRoleUser, agentRunMessageTestTask))
	}

	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, child.ID, uuid.New(), ScheduleActivityInput{
			StepID: "compact:0", Name: compactionActivityTestName,
			Kind:         models.ActivityKindCompaction,
			ReplayPolicy: models.ActivityReplayPolicyRetry,
			Input:        json.RawMessage(`{}`),
		})
	require.NoError(t, err)
	input := CompleteActivityInput{
		Output:  json.RawMessage(`{"summary":"child summary"}`),
		Context: json.RawMessage(`{"messages":["child summary"]}`),
		Compaction: &CompactionInput{
			FromMessageID: messages[0].ID, ToMessageID: messages[1].ID,
			FromSequence: 1, ToSequence: 2, SourceMessageCount: 2,
			Summary: agentRunMessageTestSummary, ModelID: agentRunTestModel,
			PromptHash: "example-prompt",
		},
	}

	return fixture, child, claim, input
}

func TestChildCompactionCheckpointReusesAfterReopen(t *testing.T) {
	t.Parallel()
	fixture, child, claim, input := executionChildCompactionFixture(t)
	_, err := fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store := newTestStore(t, handle)
	_, err = store.PauseExecution(t.Context(), fixture.sessionID, child.ID,
		executionTestPauseReason)
	require.NoError(t, err)
	repeated, err := store.CompleteExecutionActivityReceipt(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	assert.Empty(t, repeated.Events)
	assert.Equal(t, string(input.Context), repeated.Activity.ContextJSON)
	head, err := store.LatestAgentRunCompaction(t.Context(), fixture.sessionID,
		*child.AgentRunID)
	require.NoError(t, err)
	require.NotNil(t, head)
	assert.Equal(t, claim.Activity.ID, head.ID)
	history, err := store.AgentRunHistory(t.Context(), fixture.sessionID,
		*child.AgentRunID)
	require.NoError(t, err)
	require.Len(t, history.Messages, 1)
	assert.EqualValues(t, 3, history.Messages[0].Sequence)
	rootHead, err := store.LatestCompaction(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	assert.Nil(t, rootHead)
	paused, err := store.GetExecution(t.Context(), fixture.sessionID, child.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, paused.State)
	input.Compaction.Summary = "changed summary"
	_, err = store.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	input.Compaction = nil
	_, err = store.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestChildCompactionCheckpointRollsBackInvalidSources(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		change func(*CompactionInput)
	}{
		{name: "missing boundary", change: func(input *CompactionInput) {
			input.ToMessageID = uuid.New()
		}},
		{name: "count", change: func(input *CompactionInput) {
			input.SourceMessageCount++
		}},
		{name: "activity identity", change: func(input *CompactionInput) {
			input.ID = uuid.New()
		}},
		{name: "summary", change: func(input *CompactionInput) {
			input.Summary = " "
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, child, claim, input := executionChildCompactionFixture(t)
			tc.change(input.Compaction)
			_, err := fixture.store.CompleteExecutionActivity(t.Context(),
				fixture.sessionID, claim.Lease, input)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
			activity, err := fixture.store.GetExecutionActivity(t.Context(),
				fixture.sessionID, claim.Activity.ID)
			require.NoError(t, err)
			assert.Equal(t, models.ActivityStateRunning, activity.State)
			assert.Nil(t, activity.OutputJSON)
			head, err := fixture.store.LatestAgentRunCompaction(t.Context(),
				fixture.sessionID, *child.AgentRunID)
			require.NoError(t, err)
			assert.Nil(t, head)
		})
	}
}

func TestChildCompactionCheckpointRejectsSiblingHistory(t *testing.T) {
	t.Parallel()
	fixture, child, claim, input := executionChildCompactionFixture(t)
	sibling := executionChildForTest(t, fixture, fixture.root)
	foreign := appendChildMessage(t.Context(), t, fixture.store,
		fixture.sessionID, *sibling.AgentRunID, models.MessageRoleUser,
		agentRunMessageTestTask)
	input.Compaction.FromMessageID = foreign.ID
	_, err := fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	for _, execution := range []*models.Execution{child, sibling} {
		head, err := fixture.store.LatestAgentRunCompaction(t.Context(),
			fixture.sessionID, *execution.AgentRunID)
		require.NoError(t, err)
		assert.Nil(t, head)
		page, err := fixture.store.ListAgentRunMessages(t.Context(),
			fixture.sessionID, *execution.AgentRunID,
			ListAgentRunMessagesOptions{})
		require.NoError(t, err)
		for _, message := range page.Items {
			assert.Nil(t, message.CompactionID)
		}
	}
}

func TestChildCompactionCheckpointRejectsOtherActivityKind(t *testing.T) {
	t.Parallel()
	fixture, child, claim, input := executionChildCompactionFixture(t)
	repository := fixture.store.query.ExecutionActivity
	_, err := repository.WithContext(t.Context()).Where(
		repository.ID.Eq(claim.Activity.ID),
	).UpdateSimple(repository.Kind.Value(string(models.ActivityKindModel)))
	require.NoError(t, err)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	head, err := fixture.store.LatestAgentRunCompaction(t.Context(),
		fixture.sessionID, *child.AgentRunID)
	require.NoError(t, err)
	assert.Nil(t, head)
}
