package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func acceptRootInboxForTest(
	t *testing.T,
	fixture executionLoopFixture,
	prepared *preparedTurn,
) session.AcceptExecutionMessageInput {
	t.Helper()
	input := session.AcceptExecutionMessageInput{
		RequestID: uuid.New(), SourceEventID: uuid.New(),
		WorkingDirectory: prepared.workspace, Message: "continue example work",
		Parts: []session.ExecutionInboxPart{{
			SkillName: "example", Content: "retained original skill content",
		}},
		Capacity: 32,
	}
	_, err := fixture.base.store.AcceptExecutionMessage(t.Context(),
		fixture.input.ExecutionID, prepared.lease, input)
	require.NoError(t, err)

	return input
}

func TestRootInboxModelRetryUsesSavedIDsAfterColdRestoration(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Turn{Err: context.Canceled},
		elelemtest.Text("continued after interruption"),
	)
	fixture, original := newRootModelWorkflowFixture(t, driver)
	first := acceptRootInboxForTest(t, fixture, original)
	prepared, err := fixture.engine.prepareRound(t.Context(), workflowRoundInput{
		Execution: fixture.input,
	})
	require.NoError(t, err)
	input := fixture.input
	input.Context = prepared.Output
	_, err = fixture.engine.modelRound(t.Context(), workflowRoundInput{
		Execution: input,
	})
	require.ErrorIs(t, err, context.Canceled)
	late := acceptRootInboxForTest(t, fixture, original)
	fixture.base.store.ReleaseTurn(original.lease)
	holder := session.RestoreExecutionTurnInput{OwnerGenerationID: uuid.New()}
	restored, scope, err := fixture.base.runtime.restoreRootWorkflowTurn(
		t.Context(), input, holder, nil)
	require.NoError(t, err)
	fixture.engine.options.GenerationID = holder.OwnerGenerationID
	fixture.engine.options.ModelRound = func(
		ctx context.Context, round workflowRoundInput,
	) (workflowRoundResult, error) {
		return fixture.base.runtime.workflowRootModelRound(ctx, restored, round)
	}
	result, err := fixture.engine.modelRound(t.Context(), workflowRoundInput{
		Execution: input,
	})
	require.NoError(t, err)
	requests := driver.Requests()
	require.Len(t, requests, 2)
	assert.Equal(t, requests[0].Messages, requests[1].Messages)
	assert.Contains(t, messageTexts(requests[1].Messages), first.Parts[0].Content)
	page, err := fixture.base.store.ListExecutionMessages(t.Context(),
		input.SessionID, input.ExecutionID, session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.Equal(t, models.InboxMessageStateDelivered, page.Items[0].State)
	assert.Equal(t, models.InboxMessageStateQueued, page.Items[1].State)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(result.Output.Context, &conversation))
	assert.Empty(t, conversation.InboxIDs)
	require.NoError(t, fixture.base.runtime.prepareRootWorkflowInbox(t.Context(),
		input, &conversation, scope.context().skills))
	assert.Equal(t, []uuid.UUID{late.RequestID}, conversation.InboxIDs)
	events, err := fixture.base.store.ListEvents(t.Context(), input.SessionID,
		session.ListEventsOptions{EventTypes: []string{
			EventTypeUserMessageDelivered, EventTypeSkillInjected,
		}})
	require.NoError(t, err)
	assert.Len(t, events.Items, 2)
}

func TestRootInboxCompletedModelDoesNotRedeliver(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("finished example"))
	fixture, original := newRootModelWorkflowFixture(t, driver)
	accepted := acceptRootInboxForTest(t, fixture, original)
	prepared, err := fixture.engine.prepareRound(t.Context(), workflowRoundInput{
		Execution: fixture.input,
	})
	require.NoError(t, err)
	input := fixture.input
	input.Context = prepared.Output
	_, err = fixture.engine.modelRound(t.Context(), workflowRoundInput{
		Execution: input,
	})
	require.NoError(t, err)
	reused, err := fixture.engine.modelRound(t.Context(), workflowRoundInput{
		Execution: input,
	})
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	assert.Equal(t, 1, driver.Calls())
	messages, err := fixture.base.store.ListMessages(t.Context(), input.SessionID,
		session.ListMessagesOptions{})
	require.NoError(t, err)
	require.Len(t, messages.Items, 4)
	assert.Equal(t, accepted.Message, messages.Items[1].Content)
	assert.Equal(t, accepted.Parts[0].Content, messages.Items[2].Content)
	assert.Equal(t, messages.Items[1].Content,
		original.turn.messageSnapshot()[0].Content)
	require.NoError(t, original.turn.checkpoint(t.Context()))
	after, err := fixture.base.store.ListMessages(t.Context(), input.SessionID,
		session.ListMessagesOptions{})
	require.NoError(t, err)
	assert.Equal(t, messages.Items, after.Items)
}

func TestRootInboxPreparationIncludesEveryQueuedPage(t *testing.T) {
	t.Parallel()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	count := session.DefaultPageLimit + 1
	expected := make([]uuid.UUID, 0, count)
	for range count {
		input := session.AcceptExecutionMessageInput{
			RequestID: uuid.New(), WorkingDirectory: prepared.workspace,
			Message: "distinct request with identical text", Capacity: count,
		}
		_, err := fixture.base.store.AcceptExecutionMessage(t.Context(),
			fixture.input.ExecutionID, prepared.lease, input)
		require.NoError(t, err)
		expected = append(expected, input.RequestID)
	}

	result, err := fixture.engine.prepareRound(t.Context(), workflowRoundInput{
		Execution: fixture.input,
	})
	require.NoError(t, err)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(result.Output, &conversation))
	assert.Equal(t, expected, conversation.InboxIDs)
	assert.Len(t, conversation.Messages, count+1)
}

func TestRootInboxRefusesDeliveryWithoutActivityLease(t *testing.T) {
	t.Parallel()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	err := fixture.base.runtime.deliverRootWorkflowInbox(t.Context(), prepared,
		fixture.input, []uuid.UUID{uuid.New()})
	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
}

func TestRootInboxMalformedSkillsLeaveContextUnchanged(t *testing.T) {
	t.Parallel()
	conversation := workflowConversation{InboxIDs: []uuid.UUID{uuid.New()}}
	before := conversation
	err := appendRootInboxContext(&conversation, newSkillLedger(),
		&models.ExecutionInboxMessage{
			RequestID: uuid.New(), Message: "accepted message", PartsJSON: `{`,
		})
	require.Error(t, err)
	assert.Equal(t, before, conversation)
}
