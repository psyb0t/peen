package agent

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowChildInboxReconstructsAcceptedIDsAndSkillSlots(t *testing.T) {
	t.Parallel()
	fixture, input, conversation, _ := childWorkflowInboxFixture(t)
	store := fixture.base.store
	inbox, err := newWorkflowChildInbox(t.Context(), store, store,
		input.Execution.SessionID, input.Execution.ExecutionID,
		conversation.Child.AgentRunID)
	require.NoError(t, err)
	require.Len(t, inbox.pending, 1)
	for _, count := range inbox.pending {
		assert.Equal(t, 2, count)
	}
}

func TestWorkflowChildInboxAcceptanceSignalsOnceAndCloseRetainsRows(
	t *testing.T,
) {
	t.Parallel()
	fixture, input, conversation, _ := childWorkflowInboxFixture(t)
	store := fixture.base.store
	inbox, err := newWorkflowChildInbox(t.Context(), store, store,
		input.Execution.SessionID, input.Execution.ExecutionID,
		conversation.Child.AgentRunID)
	require.NoError(t, err)
	count, arrived := inbox.arrival()
	assert.Equal(t, 2, count)
	accepted := session.AcceptExecutionMessageInput{
		RequestID: uuid.New(), WorkingDirectory: fixture.base.workspace,
		Message: "another child message", Capacity: 3,
	}
	require.NoError(t, inbox.accept(t.Context(), accepted))
	select {
	case <-arrived:
	default:
		t.Fatal("child acceptance did not signal arrival")
	}
	require.NoError(t, inbox.accept(t.Context(), accepted))
	count, _ = inbox.arrival()
	assert.Equal(t, 3, count)
	inbox.close()
	accepted.RequestID = uuid.New()
	require.ErrorIs(t, inbox.accept(t.Context(), accepted), commerr.ErrInvalidState)
	fixture.stop()
	require.NoError(t, fixture.base.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{
		Directory: fixture.base.stateDirectory,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store, err = session.NewStore(handle, session.Options{})
	require.NoError(t, err)
	restored, err := newWorkflowChildInbox(t.Context(), store, store,
		input.Execution.SessionID, input.Execution.ExecutionID,
		conversation.Child.AgentRunID)
	require.NoError(t, err)
	count, _ = restored.arrival()
	assert.Equal(t, 3, count)
}

func TestWorkflowChildInboxModelClearsOnlyCommittedDeliveries(t *testing.T) {
	t.Parallel()
	fixture, input, sink := childModelFixture(t, elelemtest.Text("child reply"))
	inbox, err := newAgentInbox(sink, 2)
	require.NoError(t, err)
	require.NoError(t, inbox.bindWorkflow(t.Context(), input.Execution.ExecutionID,
		fixture.base.workspace))
	sink.run.inbox = inbox
	require.NoError(t, inbox.deliver(t.Context(), "message for child model"))
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(input.Execution.Context, &conversation))
	scope := childPreparationScopeForTest(t, fixture, conversation)
	input.Execution.Context, err = fixture.base.runtime.prepareChildWorkflowRound(
		t.Context(), scope, input)
	require.NoError(t, err)
	_, err = fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	count, _ := inbox.arrival()
	assert.Zero(t, count)
	page, err := fixture.base.store.ListExecutionMessages(t.Context(), sink.sessionID,
		input.Execution.ExecutionID, session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.InboxMessageStateDelivered, page.Items[0].State)
}

func TestWorkflowChildInboxReadsEveryQueuedPageAndPreservesPause(t *testing.T) {
	t.Parallel()
	fixture, input, sink := childModelFixture(t)
	count := session.DefaultPageLimit + 1
	for range count {
		_, err := fixture.base.store.AcceptChildExecutionMessage(t.Context(),
			sink.sessionID, input.Execution.ExecutionID,
			session.AcceptExecutionMessageInput{
				RequestID: uuid.New(), WorkingDirectory: fixture.base.workspace,
				Message: "retained queued message", Capacity: count,
			})
		require.NoError(t, err)
	}
	_, err := fixture.base.store.PauseExecution(t.Context(), sink.sessionID,
		input.Execution.ExecutionID, "leave queued child paused")
	require.NoError(t, err)
	before, err := fixture.base.store.GetExecution(t.Context(), sink.sessionID,
		input.Execution.ExecutionID)
	require.NoError(t, err)
	inbox, err := newWorkflowChildInbox(t.Context(), fixture.base.store,
		fixture.base.store, sink.sessionID, input.Execution.ExecutionID, sink.run.ID)
	require.NoError(t, err)
	waiting, _ := inbox.arrival()
	assert.Equal(t, count, waiting)
	after, err := fixture.base.store.GetExecution(t.Context(), sink.sessionID,
		input.Execution.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	_, err = newWorkflowChildInbox(t.Context(), fixture.base.store,
		fixture.base.store, sink.sessionID, input.Execution.ExecutionID, uuid.New())
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestWorkflowChildInboxBindsExistingQueueAndDeliversDurably(t *testing.T) {
	t.Parallel()
	fixture, input, sink := childModelFixture(t)
	inbox, err := newAgentInbox(sink, 2)
	require.NoError(t, err)
	require.NoError(t, inbox.bindWorkflow(t.Context(), input.Execution.ExecutionID,
		fixture.base.workspace))
	require.NoError(t, inbox.deliver(t.Context(), "message through registry inbox"))
	count, _ := inbox.arrival()
	assert.Equal(t, 1, count)
	page, err := fixture.base.store.ListExecutionMessages(t.Context(), sink.sessionID,
		input.Execution.ExecutionID, session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "message through registry inbox", page.Items[0].Message)
	inbox.markWorkflowDelivered([]uuid.UUID{page.Items[0].RequestID})
	count, _ = inbox.arrival()
	assert.Zero(t, count)
	assert.Empty(t, inbox.close())
	restored, err := newWorkflowChildInbox(t.Context(), fixture.base.store,
		fixture.base.store, sink.sessionID, input.Execution.ExecutionID, sink.run.ID)
	require.NoError(t, err)
	count, _ = restored.arrival()
	assert.Equal(t, 1, count)
}

func TestWorkflowChildInboxRefreshesAcceptanceDirectory(t *testing.T) {
	t.Parallel()
	fixture, input, sink := childModelFixture(t)
	inbox, err := newWorkflowChildInbox(t.Context(), fixture.base.store,
		fixture.base.store, sink.sessionID, input.Execution.ExecutionID,
		sink.run.ID)
	require.NoError(t, err)
	require.NoError(t, inbox.deliverText(t.Context(), "before hop", 2))
	directory := fixture.base.workspace + "/child-directory"
	require.NoError(t, inbox.refreshDirectory(directory))
	require.NoError(t, inbox.deliverText(t.Context(), "after hop", 2))
	page, err := fixture.base.store.ListExecutionMessages(t.Context(),
		sink.sessionID, input.Execution.ExecutionID,
		session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.Equal(t, fixture.base.workspace, page.Items[0].WorkingDirectory)
	assert.Equal(t, directory, page.Items[1].WorkingDirectory)
	inbox.close()
	require.ErrorIs(t, inbox.refreshDirectory(fixture.base.workspace),
		commerr.ErrInvalidState)
}
