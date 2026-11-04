package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// endedQueueWaitTestTimeout bounds a wait that is expected to time out.
const endedQueueWaitTestTimeout = 200 * time.Millisecond

func TestWorkflowQueueRestorationRetainsPendingArrivalAndReplacesOldQueue(
	t *testing.T,
) {
	t.Parallel()
	fixture, original := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	input := TurnRequest{RequestID: uuid.New(), Message: "continue saved work"}
	require.NoError(t, original.userMessages.enqueue(t.Context(), input))
	fixture.base.store.ReleaseTurn(original.lease)
	restored, scope, err := fixture.base.runtime.restoreRootWorkflowTurn(
		t.Context(), fixture.input, session.RestoreExecutionTurnInput{
			OwnerGenerationID: uuid.New(),
		}, nil)
	require.NoError(t, err)
	assert.NotSame(t, original.userMessages, restored.userMessages)
	assert.Same(t, restored.userMessages, scope.options.queue)
	count, _ := restored.userMessages.arrival()
	assert.Equal(t, 1, count)
	require.ErrorIs(t, original.userMessages.enqueue(t.Context(), input),
		commerr.ErrConflict)
	result, queued, err := fixture.base.runtime.queueActiveUserMessage(
		t.Context(), TurnRequest{
			RequestID: uuid.New(),
			SessionID: &fixture.input.SessionID, Message: "new request",
		})
	require.NoError(t, err)
	assert.True(t, queued)
	require.NotNil(t, result)
	assert.True(t, result.Queued)
	count, _ = restored.userMessages.arrival()
	assert.Equal(t, 2, count)
}

// A message that reaches a queue whose turn just ended waits for that queue
// to go, then starts the next turn instead of failing.
func TestQueuedMessageForAnEndedTurnStartsTheNextTurn(t *testing.T) {
	t.Parallel()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	fixture.base.store.ReleaseTurn(prepared.lease)

	type outcome struct {
		result  *TurnResult
		handled bool
		err     error
	}

	done := make(chan outcome, 1)
	go func() {
		result, handled, err := fixture.base.runtime.queueActiveUserMessage(
			t.Context(), TurnRequest{
				RequestID: uuid.New(), SessionID: &fixture.input.SessionID,
				Message: "next request",
			})
		done <- outcome{result: result, handled: handled, err: err}
	}()

	fixture.base.runtime.closeActiveUserMessageQueue(prepared)

	got := <-done
	require.NoError(t, got.err)
	assert.False(t, got.handled)
	assert.Nil(t, got.result)
	count, _ := prepared.userMessages.arrival()
	assert.Zero(t, count)
}

// While the ended turn's queue is still registered, the message waits for it
// and gives up only when its own context ends.
func TestQueuedMessageForAnEndedTurnWaitsForItsQueue(t *testing.T) {
	t.Parallel()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	fixture.base.store.ReleaseTurn(prepared.lease)

	ctx, cancel := context.WithTimeout(t.Context(), endedQueueWaitTestTimeout)
	defer cancel()

	_, handled, err := fixture.base.runtime.queueActiveUserMessage(ctx,
		TurnRequest{
			RequestID: uuid.New(), SessionID: &fixture.input.SessionID,
			Message: "next request",
		})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.True(t, handled)
	count, _ := prepared.userMessages.arrival()
	assert.Zero(t, count)
}

func TestWorkflowQueueMalformedHydrationDoesNotPartlyChangeSkills(t *testing.T) {
	t.Parallel()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	first := session.AcceptExecutionMessageInput{
		RequestID: uuid.New(), WorkingDirectory: prepared.workspace,
		Message: "first retained input", Capacity: 4,
		Parts: []session.ExecutionInboxPart{{
			SkillName: "exampleRetainedSkill", Content: "saved skill body",
		}},
	}
	_, err := fixture.base.store.AcceptExecutionMessage(t.Context(),
		fixture.input.ExecutionID, prepared.lease, first)
	require.NoError(t, err)
	second := first
	second.RequestID = uuid.New()
	second.Parts = nil
	_, err = fixture.base.store.AcceptExecutionMessage(t.Context(),
		fixture.input.ExecutionID, prepared.lease, second)
	require.NoError(t, err)
	query := repositories.Use(fixture.base.handle.GormDB)
	repository := query.ExecutionInboxMessage
	_, err = repository.WithContext(t.Context()).
		Where(repository.RequestID.Eq(second.RequestID)).
		Update(repository.PartsJSON, `[1]`)
	require.NoError(t, err)
	queue := prepared.userMessages
	before := queue.skills.held()
	err = queue.bindWorkflowInbox(t.Context(), fixture.input.ExecutionID)
	var invalid *json.UnmarshalTypeError
	require.ErrorAs(t, err, &invalid)
	assert.Equal(t, before, queue.skills.held())
	count, _ := queue.arrival()
	assert.Zero(t, count)
}
