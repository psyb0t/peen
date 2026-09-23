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

const (
	agentRunMessageTestTask    = "child task"
	agentRunMessageTestReply   = "child reply"
	agentRunMessageTestSummary = "child summary"
)

func appendChildMessage(
	ctx context.Context,
	t *testing.T,
	store *Store,
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	role models.MessageRole,
	content string,
) *models.AgentRunMessage {
	t.Helper()

	stored, err := store.AppendAgentRunMessages(
		ctx,
		sessionID,
		agentRunID,
		[]AgentRunMessageInput{{Role: role, Content: content}},
	)
	require.NoError(t, err)
	require.Len(t, stored, 1)

	return stored[0]
}

// Two children of the same session keep separate transcripts, each with its
// own sequence, and neither can be read through the other session.
func TestStoreAgentRunTranscriptsAreSeparatePerRun(t *testing.T) {
	ctx := context.Background()
	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	owner := newTestSession(ctx, t, store)
	other := newTestSession(ctx, t, store)
	lease := startAgentRunTestTurn(ctx, t, store, owner.ID)

	first := createAgentRunForTest(ctx, t, store, owner.ID, lease.TurnID, 0)
	second := createAgentRunForTest(ctx, t, store, owner.ID, lease.TurnID, 1)

	for _, run := range []*models.AgentRun{first, second} {
		appendChildMessage(
			ctx,
			t,
			store,
			owner.ID,
			run.ID,
			models.MessageRoleUser,
			agentRunMessageTestTask,
		)
		appendChildMessage(
			ctx,
			t,
			store,
			owner.ID,
			run.ID,
			models.MessageRoleAssistant,
			agentRunMessageTestReply,
		)
	}

	for _, run := range []*models.AgentRun{first, second} {
		page, err := store.ListAgentRunMessages(
			ctx,
			owner.ID,
			run.ID,
			ListAgentRunMessagesOptions{},
		)
		require.NoError(t, err)
		require.Len(t, page.Items, 2)
		assert.Equal(t, int64(1), page.Items[0].Sequence)
		assert.Equal(t, int64(2), page.Items[1].Sequence)

		for _, message := range page.Items {
			assert.Equal(t, run.ID, message.AgentRunID)
		}
	}

	// The session transcript is a different table, so a child writing its own
	// conversation adds nothing to the parent's.
	parent, err := store.ListMessages(ctx, owner.ID, ListMessagesOptions{
		Limit: MaximumPageLimit,
		Order: PageOrderAscending,
	})
	require.NoError(t, err)

	for _, message := range parent.Items {
		assert.NotEqual(t, agentRunMessageTestReply, message.Content)
	}

	_, err = store.ListAgentRunMessages(
		ctx,
		other.ID,
		first.ID,
		ListAgentRunMessagesOptions{},
	)
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

// A second child compaction covers only what it read itself, supersedes the
// first, and leaves the first row's direct membership alone. Reconstruction
// then returns the newest summary plus the raw tail after it.
func TestStoreAgentRunCompactionChainKeepsDirectMembership(t *testing.T) {
	ctx := context.Background()
	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	owner := newTestSession(ctx, t, store)
	lease := startAgentRunTestTurn(ctx, t, store, owner.ID)
	run := createAgentRunForTest(ctx, t, store, owner.ID, lease.TurnID, 0)

	messages := make([]*models.AgentRunMessage, 0, 6)
	for index := range 6 {
		role := models.MessageRoleUser
		if index%2 == 1 {
			role = models.MessageRoleAssistant
		}

		messages = append(messages, appendChildMessage(
			ctx,
			t,
			store,
			owner.ID,
			run.ID,
			role,
			agentRunMessageTestReply,
		))
	}

	first, err := store.CreateAgentRunCompaction(
		ctx,
		owner.ID,
		run.ID,
		AgentRunCompactionInput{
			FromMessageID:      messages[0].ID,
			ToMessageID:        messages[1].ID,
			FromSequence:       messages[0].Sequence,
			ToSequence:         messages[1].Sequence,
			DirectFromSequence: messages[0].Sequence,
			DirectToSequence:   messages[1].Sequence,
			Summary:            agentRunMessageTestSummary,
			SourceMessageCount: 2,
			ModelID:            agentRunTestModel,
		},
	)
	require.NoError(t, err)
	assert.Nil(t, first.SupersedesCompactionID)

	second, err := store.CreateAgentRunCompaction(
		ctx,
		owner.ID,
		run.ID,
		AgentRunCompactionInput{
			FromMessageID:          messages[0].ID,
			ToMessageID:            messages[3].ID,
			FromSequence:           messages[0].Sequence,
			ToSequence:             messages[3].Sequence,
			DirectFromSequence:     messages[2].Sequence,
			DirectToSequence:       messages[3].Sequence,
			Summary:                agentRunMessageTestSummary,
			SourceMessageCount:     4,
			ModelID:                agentRunTestModel,
			SupersedesCompactionID: &first.ID,
		},
	)
	require.NoError(t, err)
	require.NotNil(t, second.SupersedesCompactionID)
	assert.Equal(t, first.ID, *second.SupersedesCompactionID)

	// The first row is immutable: the second one covering the same lineage
	// span must not have rewritten what the first recorded directly.
	reread, err := store.GetAgentRunCompaction(ctx, owner.ID, run.ID, first.ID)
	require.NoError(t, err)
	assert.Equal(t, messages[0].Sequence, reread.DirectFromSequence)
	assert.Equal(t, messages[1].Sequence, reread.DirectToSequence)
	assert.Nil(t, reread.SupersedesCompactionID)

	stored, err := store.ListAgentRunMessages(
		ctx,
		owner.ID,
		run.ID,
		ListAgentRunMessagesOptions{Limit: MaximumPageLimit},
	)
	require.NoError(t, err)
	require.Len(t, stored.Items, len(messages))

	for index, message := range stored.Items {
		switch {
		case index < 2:
			require.NotNil(t, message.CompactionID)
			assert.Equal(t, first.ID, *message.CompactionID)
		case index < 4:
			require.NotNil(t, message.CompactionID)
			assert.Equal(t, second.ID, *message.CompactionID)
		default:
			assert.Nil(t, message.CompactionID)
		}
	}

	history, err := store.AgentRunHistory(ctx, owner.ID, run.ID)
	require.NoError(t, err)
	require.NotNil(t, history.Compaction)
	assert.Equal(t, second.ID, history.Compaction.ID)
	require.Len(t, history.Messages, 2)
	assert.Equal(t, messages[4].ID, history.Messages[0].ID)
	assert.Equal(t, messages[5].ID, history.Messages[1].ID)

	head, err := store.LatestAgentRunCompaction(ctx, owner.ID, run.ID)
	require.NoError(t, err)
	require.NotNil(t, head)
	assert.Equal(t, second.ID, head.ID)
}

// A process that stops mid-child leaves the child's own transcript marked
// incomplete, the same way an interrupted turn marks the session's.
func TestStoreRecoverInterruptedAgentRunsMarksChildMessages(t *testing.T) {
	ctx := context.Background()
	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	owner := newTestSession(ctx, t, store)
	lease := startAgentRunTestTurn(ctx, t, store, owner.ID)
	run := createAgentRunForTest(ctx, t, store, owner.ID, lease.TurnID, 0)

	appendChildMessage(
		ctx,
		t,
		store,
		owner.ID,
		run.ID,
		models.MessageRoleUser,
		agentRunMessageTestTask,
	)
	appendChildMessage(
		ctx,
		t,
		store,
		owner.ID,
		run.ID,
		models.MessageRoleAssistant,
		agentRunMessageTestReply,
	)

	recovered, err := store.RecoverInterruptedAgentRuns(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, recovered)

	stored, err := store.GetAgentRun(ctx, owner.ID, run.ID)
	require.NoError(t, err)
	assert.Equal(t, models.AgentRunStateInterrupted, stored.State)

	page, err := store.ListAgentRunMessages(
		ctx,
		owner.ID,
		run.ID,
		ListAgentRunMessagesOptions{Limit: MaximumPageLimit},
	)
	require.NoError(t, err)
	require.Len(t, page.Items, 2)

	for _, message := range page.Items {
		assert.True(
			t,
			message.Incomplete,
			"an interrupted child's records must read as incomplete",
		)
	}
}
