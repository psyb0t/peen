package agent

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func deferredRootFixture(t *testing.T) (
	executionLoopFixture, *models.ExecutionInboxMessage, *workflowDispatch,
) {
	t.Helper()
	fixture, original, dispatch := newRootSubmissionFixture(t)
	input, err := dispatch.prepareRootSubmission(t.Context(), original)
	require.NoError(t, err)
	store := fixture.base.store
	hook, err := store.BeginExecutionActivity(t.Context(), input.SessionID,
		input.ExecutionID, fixture.engine.options.GenerationID,
		session.ScheduleActivityInput{
			StepID:       "terminalHooks:0",
			Name:         session.ExecutionTerminalHooksActivityName,
			Kind:         models.ActivityKindTool,
			ReplayPolicy: models.ActivityReplayPolicyReconcile,
			Input:        json.RawMessage(`{}`),
		})
	require.NoError(t, err)
	_, err = store.ReserveExecutionCompletion(t.Context(), input.SessionID,
		hook.Lease)
	require.NoError(t, err)
	accepted, err := store.DeferExecutionMessage(t.Context(), input.ExecutionID,
		original.lease, session.AcceptExecutionMessageInput{
			RequestID: uuid.New(), WorkingDirectory: fixture.base.workspace,
			Message: "continue :gone", Capacity: 2,
			Parts: []session.ExecutionInboxPart{{
				SkillName: "gone",
				Content:   "frozen removed skill instructions",
			}},
		})
	require.NoError(t, err)
	result := session.CompleteActivityInput{
		Output: json.RawMessage(`{}`), Context: input.Context,
	}
	_, err = store.CompleteExecutionActivity(t.Context(), input.SessionID,
		hook.Lease, result)
	require.NoError(t, err)
	final, err := store.BeginExecutionActivity(t.Context(), input.SessionID,
		input.ExecutionID, fixture.engine.options.GenerationID,
		session.ScheduleActivityInput{
			StepID:       session.ExecutionFinishStepID,
			Name:         session.ExecutionFinishActivityName,
			Kind:         models.ActivityKindPreparation,
			ReplayPolicy: models.ActivityReplayPolicyRetry,
			Input:        json.RawMessage(`{}`),
		})
	require.NoError(t, err)
	result.Finish = &session.FinishExecutionInput{
		State:      models.ExecutionStateCompleted,
		RootHolder: &session.ExecutionFinishHolder{LeaseID: original.lease.ID},
		Turn:       &session.FinalizeTurnInput{State: models.TurnStateCompleted},
	}
	_, err = store.CompleteExecutionActivity(t.Context(), input.SessionID,
		final.Lease, result)
	require.NoError(t, err)
	fixture.base.runtime.closeActiveUserMessageQueue(original)

	return fixture, accepted.Message, dispatch
}

func TestDeferredRootPreparesFrozenRemovedSkillAndLinksSubmission(t *testing.T) {
	t.Parallel()
	fixture, message, dispatch := deferredRootFixture(t)
	fixture.base.runtime.defaultModel = "unavailable"
	prepared, err := fixture.base.runtime.prepareDeferredRoot(t.Context(),
		message.ExecutionID, message.RequestID, nil)
	require.NoError(t, err)
	t.Cleanup(func() { fixture.base.store.ReleaseTurn(prepared.lease) })
	assert.Equal(t, message.RequestID, prepared.input.RequestID)
	assert.Equal(t, message.Message, prepared.input.Message)
	assert.Equal(t, EventTypeUserMessageDelivered, prepared.turn.events[0].Type)
	assert.NotEqual(t, "unavailable", prepared.modelReference)
	require.NotNil(t, prepared.deferredInboxID)
	assert.Equal(t, message.ID, *prepared.deferredInboxID)
	encoded, err := json.Marshal(prepared.prompt.Messages())
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "frozen removed skill instructions")
	require.NoError(t,
		fixture.base.runtime.registerActiveUserMessageQueue(prepared))
	t.Cleanup(func() {
		fixture.base.runtime.closeActiveUserMessageQueue(prepared)
	})
	input, err := dispatch.prepareRootSubmission(t.Context(), prepared)
	require.NoError(t, err)
	row, err := fixture.base.store.GetExecutionMessageByRequest(t.Context(),
		message.SessionID, message.ExecutionID, message.RequestID)
	require.NoError(t, err)
	assert.Equal(t, models.InboxMessageStateForwarded, row.State)
	require.NotNil(t, row.ForwardedExecutionID)
	assert.Equal(t, input.ExecutionID, *row.ForwardedExecutionID)
}

func TestNormalRootStillRejectsRemovedSkill(t *testing.T) {
	t.Parallel()
	fixture, _, _ := deferredRootFixture(t)
	_, err := fixture.base.runtime.prepareTurn(t.Context(), TurnRequest{
		RequestID: uuid.New(), Workspace: fixture.base.workspace,
		Message: "continue :gone",
	})
	require.ErrorIs(t, err, harness.ErrSkillNotFound)
}

func TestDeferredRootRejectsInvalidSavedSkillParts(t *testing.T) {
	t.Parallel()
	fixture, message, _ := deferredRootFixture(t)
	repository := repositories.Use(fixture.base.handle.GormDB).
		ExecutionInboxMessage
	_, err := repository.WithContext(t.Context()).Where(
		repository.ID.Eq(message.ID),
	).UpdateSimple(repository.PartsJSON.Value(`[{}]`))
	require.NoError(t, err)
	_, err = fixture.base.runtime.prepareDeferredRoot(t.Context(),
		message.ExecutionID, message.RequestID, nil)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	assert.False(t, fixture.base.store.IsActive(message.SessionID))
}

func TestDeferredRootRejectsForeignSavedIdentity(t *testing.T) {
	t.Parallel()
	fixture, message, _ := deferredRootFixture(t)
	source, err := fixture.base.store.GetExecution(t.Context(),
		message.SessionID, message.ExecutionID)
	require.NoError(t, err)
	testCases := []struct {
		name   string
		change func(*workflowConversation)
	}{
		{name: "session", change: func(saved *workflowConversation) {
			saved.Root.SessionID = uuid.New()
		}},
		{name: "turn", change: func(saved *workflowConversation) {
			saved.Root.TurnID = uuid.New()
		}},
		{name: "version", change: func(saved *workflowConversation) {
			saved.Root.Version++
		}},
		{name: "missing bootstrap", change: func(saved *workflowConversation) {
			saved.Root = nil
		}},
		{name: "missing model", change: func(saved *workflowConversation) {
			saved.ModelReference = ""
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var conversation workflowConversation
			require.NoError(t, json.Unmarshal([]byte(source.ContextJSON),
				&conversation))
			tc.change(&conversation)
			encoded, err := json.Marshal(conversation)
			require.NoError(t, err)
			changed := *source
			changed.ContextJSON = string(encoded)
			_, err = decodeDeferredRoot(&changed, message, nil)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}

func TestDeferredRootRejectsWrongStateBeforeOpeningTurn(t *testing.T) {
	t.Parallel()
	fixture, message, _ := deferredRootFixture(t)
	repository := repositories.Use(fixture.base.handle.GormDB).
		ExecutionInboxMessage
	_, err := repository.WithContext(t.Context()).Where(
		repository.ID.Eq(message.ID),
	).UpdateSimple(repository.State.Value(string(models.InboxMessageStateQueued)))
	require.NoError(t, err)
	_, err = fixture.base.runtime.prepareDeferredRoot(t.Context(),
		message.ExecutionID, message.RequestID, nil)
	require.ErrorIs(t, err, commerr.ErrConflict)
	assert.False(t, fixture.base.store.IsActive(message.SessionID))
}
