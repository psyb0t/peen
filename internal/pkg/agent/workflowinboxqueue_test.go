package agent

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
