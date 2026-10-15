package session

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentRunCompactionLeavesIncompleteGapUnassigned(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	owner := newTestSession(ctx, t, store)
	lease := startAgentRunTestTurn(ctx, t, store, owner.ID)
	run := createAgentRunForTest(ctx, t, store, owner.ID, lease.TurnID, 0)
	messages := make([]*models.AgentRunMessage, 0, 3)
	for range 3 {
		messages = append(messages, appendChildMessage(ctx, t, store,
			owner.ID, run.ID, models.MessageRoleUser, agentRunMessageTestTask))
	}

	repository := store.query.AgentRunMessage
	_, err := repository.WithContext(ctx).Where(
		repository.ID.Eq(messages[1].ID),
	).UpdateSimple(repository.Incomplete.Value(true))
	require.NoError(t, err)
	compaction, err := store.CreateAgentRunCompaction(ctx, owner.ID, run.ID,
		AgentRunCompactionInput{
			FromMessageID: messages[0].ID, ToMessageID: messages[2].ID,
			FromSequence: 1, ToSequence: 3, SourceMessageCount: 2,
			Summary: agentRunMessageTestSummary,
		})
	require.NoError(t, err)
	page, err := store.ListAgentRunMessages(ctx, owner.ID, run.ID,
		ListAgentRunMessagesOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 3)
	assert.Equal(t, &compaction.ID, page.Items[0].CompactionID)
	assert.Nil(t, page.Items[1].CompactionID)
	assert.True(t, page.Items[1].Incomplete)
	assert.Equal(t, agentRunMessageTestTask, page.Items[1].Content)
	assert.Equal(t, &compaction.ID, page.Items[2].CompactionID)
}

func TestAgentRunCompactionPredecessorBoundaries(t *testing.T) {
	t.Parallel()
	head := &models.AgentRunCompaction{
		ID: uuid.New(), FromMessageID: uuid.New(),
		FromSequence: 1, ToSequence: 2, SourceMessageCount: 2,
	}
	testCases := []struct {
		name   string
		change func(*AgentRunCompactionInput)
	}{
		{name: "missing", change: func(input *AgentRunCompactionInput) {
			input.SupersedesCompactionID = nil
		}},
		{name: "stale", change: func(input *AgentRunCompactionInput) {
			id := uuid.New()
			input.SupersedesCompactionID = &id
		}},
		{name: "first message", change: func(input *AgentRunCompactionInput) {
			input.FromMessageID = uuid.New()
		}},
		{name: "first sequence", change: func(input *AgentRunCompactionInput) {
			input.FromSequence++
		}},
		{name: "overlap", change: func(input *AgentRunCompactionInput) {
			input.DirectFromSequence = head.ToSequence
		}},
		{name: "last sequence", change: func(input *AgentRunCompactionInput) {
			input.DirectToSequence--
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := AgentRunCompactionInput{
				SupersedesCompactionID: &head.ID, FromMessageID: head.FromMessageID,
				FromSequence: 1, ToSequence: 4,
				DirectFromSequence: 3, DirectToSequence: 4,
			}
			tc.change(&input)
			_, _, err := agentRunCompactionPredecessor(head, input)
			require.ErrorIs(t, err, commerr.ErrConflict)
		})
	}
}
