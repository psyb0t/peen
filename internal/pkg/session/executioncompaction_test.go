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

const (
	compactionActivityTestName = "compactHistoryV1"

	// unfinishedTurnRows is what one unfinished fixture turn stores: its
	// request, its unfinished answer, and the system message it ends with.
	unfinishedTurnRows = 3
)

func executionCompactionFixture(
	t *testing.T,
	gapStates ...models.TurnState,
) (
	executionFixture, *ActivityClaim, CompleteActivityInput,
) {
	t.Helper()
	fixture := executionFixture{directory: t.TempDir()}
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	fixture.handle = handle
	fixture.store = newTestStore(t, handle)
	created := newTestSession(t.Context(), t, fixture.store)
	fixture.sessionID = created.ID
	first := completeMessage(t.Context(), t, fixture.store, created.ID,
		"first completed answer")
	for _, state := range gapStates {
		lease, err := fixture.store.AcquireTurn(t.Context(), created.ID,
			StartTurnInput{RequestID: uuid.New(), Workspace: testWorkspace})
		require.NoError(t, err)
		require.NoError(t, fixture.store.FinalizeTurn(t.Context(), lease,
			FinalizeTurnInput{State: state, Messages: []MessageInput{
				{Role: models.MessageRoleUser, Content: "interrupted request"},
				{Role: models.MessageRoleAssistant, Content: "unfinished answer"},
			}}))
	}
	last := completeMessage(t.Context(), t, fixture.store, created.ID,
		"second completed answer")
	lease := startAgentRunTestTurn(t.Context(), t, fixture.store, created.ID)
	fixture.turnID = lease.TurnID
	fixture.root, err = fixture.store.CreateExecution(t.Context(), created.ID,
		executionTestInput(lease.TurnID))
	require.NoError(t, err)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(), created.ID,
		fixture.root.ID, uuid.New(), ScheduleActivityInput{
			StepID: "compact:0", Name: compactionActivityTestName,
			Kind:         models.ActivityKindCompaction,
			ReplayPolicy: models.ActivityReplayPolicyRetry,
			Input:        json.RawMessage(`{}`),
		})
	require.NoError(t, err)
	// Each unfinished turn holds its two messages and the system message
	// Peen added when it ended.
	unfinished := unfinishedTurnRows * int64(len(gapStates))
	input := CompleteActivityInput{
		Output:  json.RawMessage(`{"summary":"retained history"}`),
		Context: json.RawMessage(`{"messages":["retained history"]}`),
		Compaction: &CompactionInput{
			FromMessageID: first, ToMessageID: last,
			FromSequence: 1, ToSequence: 2 + unfinished,
			SourceMessageCount: 2 + unfinished,
			Summary:            "retained history", ModelID: "example/model",
			PromptHash: "example-prompt",
		},
	}

	return fixture, claim, input
}

// A turn that ended unfinished is part of history, so a summary that spans
// it covers its messages like any other turn's.
func TestCompactionActivityCoversUnfinishedTurns(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		state models.TurnState
	}{
		{name: "failed", state: models.TurnStateFailed},
		{name: "cancelled", state: models.TurnStateCancelled},
		{name: "interrupted", state: models.TurnStateInterrupted},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, claim, input := executionCompactionFixture(t, tc.state)
			_, err := fixture.store.CompleteExecutionActivity(t.Context(),
				fixture.sessionID, claim.Lease, input)
			require.NoError(t, err)
			repository := fixture.store.query.Message
			messages, err := repository.WithContext(t.Context()).Where(
				repository.SessionID.Eq(fixture.sessionID),
			).Order(repository.Sequence).Find()
			require.NoError(t, err)
			require.Len(t, messages, 6)
			assert.Equal(t, "unfinished answer", messages[2].Content)
			assert.Equal(t, models.MessageRoleSystem, messages[3].Role)

			for _, message := range messages[:5] {
				assert.Equal(t, &claim.Activity.ID, message.CompactionID)
			}

			assert.Nil(t, messages[5].CompactionID)
			history, err := fixture.store.CompletedHistory(t.Context(),
				fixture.sessionID)
			require.NoError(t, err)
			assert.Empty(t, history.Messages)
			require.NotNil(t, history.Compaction)
			assert.EqualValues(t, 2+unfinishedTurnRows,
				history.Compaction.SourceMessageCount)
		})
	}
}

func TestCompactionActivityCommitsSummaryWithCheckpoint(t *testing.T) {
	t.Parallel()
	fixture, claim, input := executionCompactionFixture(t)
	_, err := fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	head, err := fixture.store.LatestCompaction(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	require.NotNil(t, head)
	assert.Equal(t, claim.Activity.ID, head.ID)
	assert.Equal(t, input.Compaction.Summary, head.Summary)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	input.Compaction.Summary = "changed summary"
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	input.Compaction = nil
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestCompactionActivityRejectsInvalidHistory(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		change func(*CompactionInput)
	}{
		{name: "empty summary", change: func(input *CompactionInput) {
			input.Summary = " "
		}},
		{name: "foreign boundary", change: func(input *CompactionInput) {
			input.ToMessageID = uuid.New()
		}},
		{name: "wrong count", change: func(input *CompactionInput) {
			input.SourceMessageCount++
		}},
		{name: "unfinished turn", change: func(input *CompactionInput) {
			input.ToSequence = 3
			input.SourceMessageCount = 3
		}},
		{name: "different id", change: func(input *CompactionInput) {
			input.ID = uuid.New()
		}},
		{name: "negative tokens", change: func(input *CompactionInput) {
			input.InputTokenCount = -1
		}},
		{name: "negative summary tokens", change: func(input *CompactionInput) {
			input.SummaryTokenCount = -1
		}},
		{name: "empty model", change: func(input *CompactionInput) {
			input.ModelID = " "
		}},
		{name: "empty prompt hash", change: func(input *CompactionInput) {
			input.PromptHash = " "
		}},
		{name: "missing boundary", change: func(input *CompactionInput) {
			input.FromMessageID = uuid.Nil
		}},
		{name: "empty count", change: func(input *CompactionInput) {
			input.SourceMessageCount = 0
		}},
		{name: "reversed range", change: func(input *CompactionInput) {
			input.FromSequence = 3
		}},
		{name: "invalid direct range", change: func(input *CompactionInput) {
			input.DirectToSequence = 3
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, claim, input := executionCompactionFixture(t)
			tc.change(input.Compaction)
			_, err := fixture.store.CompleteExecutionActivity(t.Context(),
				fixture.sessionID, claim.Lease, input)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
			head, err := fixture.store.LatestCompaction(t.Context(),
				fixture.sessionID)
			require.NoError(t, err)
			assert.Nil(t, head)
			saved, err := fixture.store.findExecutionActivity(t.Context(),
				fixture.store.query, fixture.sessionID, claim.Activity.ID)
			require.NoError(t, err)
			assert.Equal(t, models.ActivityStateRunning, saved.State)
			assert.Nil(t, saved.OutputJSON)
		})
	}
}

func TestCompactionActivityRejectsUnfinishedSource(t *testing.T) {
	t.Parallel()
	fixture, claim, input := executionCompactionFixture(t)
	repository := fixture.store.query.Message
	message, err := repository.WithContext(t.Context()).Where(
		repository.ID.Eq(input.Compaction.FromMessageID),
	).First()
	require.NoError(t, err)
	turn := fixture.store.query.Turn
	_, err = turn.WithContext(t.Context()).Where(
		turn.ID.Eq(message.TurnID),
	).UpdateSimple(turn.State.Value(string(models.TurnStateRunning)))
	require.NoError(t, err)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	head, err := fixture.store.LatestCompaction(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	assert.Nil(t, head)
}

func TestCompactionActivityReusesAfterReopening(t *testing.T) {
	t.Parallel()
	fixture, claim, input := executionCompactionFixture(t)
	_, err := fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store := newTestStore(t, handle)
	_, err = store.PauseExecution(t.Context(), fixture.sessionID,
		fixture.root.ID, "leave saved work paused")
	require.NoError(t, err)
	_, err = store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	repository := store.query.Compaction
	count, err := repository.WithContext(t.Context()).Where(
		repository.SessionID.Eq(fixture.sessionID),
	).Count()
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)
	history, err := store.CompletedHistory(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	assert.Empty(t, history.Messages)
	require.NotNil(t, history.Compaction)
	assert.Equal(t, claim.Activity.ID, history.Compaction.ID)
	execution, err := store.GetExecution(t.Context(), fixture.sessionID,
		fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, execution.State)
}

func TestCompactionActivityRejectsWrongKind(t *testing.T) {
	t.Parallel()
	fixture, claim, input := executionCompactionFixture(t)
	repository := fixture.store.query.ExecutionActivity
	_, err := repository.WithContext(t.Context()).Where(
		repository.ID.Eq(claim.Activity.ID),
	).UpdateSimple(repository.Kind.Value(string(models.ActivityKindModel)))
	require.NoError(t, err)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	head, err := fixture.store.LatestCompaction(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	assert.Nil(t, head)
}

func TestCompactionActivityRejectsSkippedPrefix(t *testing.T) {
	t.Parallel()
	fixture, claim, input := executionCompactionFixture(t)
	input.Compaction.FromMessageID = input.Compaction.ToMessageID
	input.Compaction.FromSequence = input.Compaction.ToSequence
	input.Compaction.SourceMessageCount = 1
	_, err := fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	head, err := fixture.store.LatestCompaction(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	assert.Nil(t, head)
}

func TestCompactionActivitySupersedesCurrentHead(t *testing.T) {
	t.Parallel()
	fixture, first, input := executionCompactionFixture(t)
	last := input.Compaction.ToMessageID
	input.Compaction.ToMessageID = input.Compaction.FromMessageID
	input.Compaction.ToSequence = 1
	input.Compaction.SourceMessageCount = 1
	_, err := fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, first.Lease, input)
	require.NoError(t, err)
	second, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, first.Lease.GenerationID,
		ScheduleActivityInput{
			StepID: "compact:1", Name: compactionActivityTestName,
			Kind:         models.ActivityKindCompaction,
			ReplayPolicy: models.ActivityReplayPolicyRetry,
			Input:        json.RawMessage(`{}`),
		})
	require.NoError(t, err)
	input.Compaction.ToMessageID = last
	input.Compaction.ToSequence = 2
	input.Compaction.DirectFromSequence = 2
	input.Compaction.DirectToSequence = 2
	input.Compaction.SourceMessageCount = 2
	input.Compaction.SupersedesCompactionID = &first.Activity.ID
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, second.Lease, input)
	require.NoError(t, err)
	head, err := fixture.store.LatestCompaction(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	require.NotNil(t, head)
	assert.Equal(t, second.Activity.ID, head.ID)
	assert.Equal(t, &first.Activity.ID, head.SupersedesCompactionID)
	repository := fixture.store.query.Message
	messages, err := repository.WithContext(t.Context()).Where(
		repository.SessionID.Eq(fixture.sessionID),
	).Order(repository.Sequence).Find()
	require.NoError(t, err)
	require.Len(t, messages, 3)
	assert.Equal(t, &first.Activity.ID, messages[0].CompactionID)
	assert.Equal(t, &second.Activity.ID, messages[1].CompactionID)
	assert.Nil(t, messages[2].CompactionID)
}
