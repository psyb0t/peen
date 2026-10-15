package session

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompactionAssignmentSpansMultipleBatches(t *testing.T) {
	t.Parallel()
	store, _ := openTestStore(t)
	opened := newTestSession(t.Context(), t, store)
	lease, err := store.AcquireTurn(t.Context(), opened.ID,
		StartTurnInput{RequestID: uuid.New(), Workspace: testWorkspace})
	require.NoError(t, err)
	messages := make([]MessageInput, compactionAssignmentBatchSize+1)
	for index := range messages {
		messages[index] = MessageInput{
			ID:   uuid.New(),
			Role: models.MessageRoleUser, Content: "completed history",
		}
	}
	require.NoError(t, store.FinalizeTurn(t.Context(), lease,
		FinalizeTurnInput{State: models.TurnStateCompleted, Messages: messages}))
	compaction, err := store.CreateCompaction(t.Context(), opened.ID,
		CompactionInput{
			FromMessageID: messages[0].ID,
			ToMessageID:   messages[len(messages)-1].ID,
			FromSequence:  1, ToSequence: int64(len(messages)),
			SourceMessageCount: int64(len(messages)),
			Summary:            "retained summary", ModelID: "example/model",
			PromptHash: "example-prompt",
		})
	require.NoError(t, err)
	repository := store.query.Message
	count, err := repository.WithContext(t.Context()).Where(
		repository.SessionID.Eq(opened.ID),
		repository.CompactionID.Eq(compaction.ID),
	).Count()
	require.NoError(t, err)
	assert.EqualValues(t, len(messages), count)
}

func TestCompactionAssignmentConflictRollsBackSummaryAndLinks(t *testing.T) {
	t.Parallel()
	fixture, _, input := executionCompactionFixture(t)
	input.Compaction.ID = uuid.New()
	row := fixture.store.compactionRecord(fixture.sessionID, *input.Compaction)
	err := fixture.store.query.Transaction(func(query *repositories.Query) error {
		return createCompactionForMessages(t.Context(), query, row,
			[]uuid.UUID{
				input.Compaction.FromMessageID,
				input.Compaction.FromMessageID, input.Compaction.ToMessageID,
			})
	})
	require.ErrorIs(t, err, commerr.ErrConflict)
	head, err := fixture.store.LatestCompaction(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	assert.Nil(t, head)
	page, err := fixture.store.ListMessages(t.Context(), fixture.sessionID,
		ListMessagesOptions{})
	require.NoError(t, err)
	for _, message := range page.Items {
		assert.Nil(t, message.CompactionID)
	}
}

func TestCompactionAssignmentRejectsNoCompletedSources(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	page, err := fixture.store.ListMessages(t.Context(), fixture.sessionID,
		ListMessagesOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	message := page.Items[0]
	_, err = fixture.store.CreateCompaction(t.Context(), fixture.sessionID,
		CompactionInput{
			FromMessageID: message.ID, ToMessageID: message.ID,
			FromSequence: message.Sequence, ToSequence: message.Sequence,
			SourceMessageCount: 1, Summary: "invalid summary",
			ModelID: "example/model", PromptHash: "example-prompt",
		})
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	head, err := fixture.store.LatestCompaction(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	assert.Nil(t, head)
}
