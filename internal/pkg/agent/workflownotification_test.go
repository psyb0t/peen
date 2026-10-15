package agent

import (
	"testing"
	"time"

	"github.com/cschleiden/go-workflows/client"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowNotificationChildCompletionSignalsRootOnce(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childModelFixture(t, elelemtest.Text("child report"))
	dispatcher := newWorkflowDispatch(t.Context(), fixture.base.runtime,
		fixture.engine.options.GenerationID, nil)
	dispatcher.bind(&fixture.engine.options)
	parent, err := dispatcher.root.resolve(t.Context(), fixture.input)
	require.NoError(t, err)
	t.Cleanup(func() {
		fixture.base.runtime.closeActiveUserMessageQueue(parent.prepared)
	})
	queue := parent.prepared.userMessages
	count, arrived := queue.arrival()
	assert.Zero(t, count)
	parentID := fixture.input.ExecutionID
	fixture.input = input.Execution
	fixture.input.MaxConcurrentTools = 1
	instance := startCompactingWorkflow(t, fixture)
	_, err = client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, 10*time.Second)
	require.NoError(t, err)
	count, next := queue.arrival()
	assert.Equal(t, 1, count)
	select {
	case <-arrived:
	default:
		t.Fatal("child completion did not signal root inbox")
	}
	require.NoError(t, dispatcher.notifyParent(t.Context(), input.Execution.SessionID,
		parentID))
	_, after := queue.arrival()
	assert.Equal(t, next, after)
	select {
	case <-next:
		t.Fatal("repeated notification signalled the same report twice")
	default:
	}
	fixture.stop()
}

func TestWorkflowNotificationLeavesPausedRootUnsignalled(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childModelFixture(t, elelemtest.Text("retained report"))
	dispatcher := newWorkflowDispatch(t.Context(), fixture.base.runtime,
		fixture.engine.options.GenerationID, nil)
	dispatcher.bind(&fixture.engine.options)
	parentInput := fixture.input
	parent, err := dispatcher.root.resolve(t.Context(), parentInput)
	require.NoError(t, err)
	t.Cleanup(func() {
		fixture.base.runtime.closeActiveUserMessageQueue(parent.prepared)
	})
	_, arrived := parent.prepared.userMessages.arrival()
	_, err = fixture.base.store.PauseExecution(t.Context(), parentInput.SessionID,
		parentInput.ExecutionID, "retain paused parent")
	require.NoError(t, err)
	_, err = fixture.base.store.ResumeExecution(t.Context(), input.Execution.SessionID,
		input.Execution.ExecutionID)
	require.NoError(t, err)
	fixture.input = input.Execution
	fixture.input.MaxConcurrentTools = 1
	instance := startCompactingWorkflow(t, fixture)
	_, err = client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, 10*time.Second)
	require.NoError(t, err)
	stored, err := fixture.base.store.GetExecution(t.Context(), parentInput.SessionID,
		parentInput.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, stored.State)
	assert.Equal(t, int64(1), stored.ControlRevision)
	count, after := parent.prepared.userMessages.arrival()
	assert.Zero(t, count)
	assert.Equal(t, arrived, after)
	select {
	case <-arrived:
		t.Fatal("child report signalled paused parent")
	default:
	}
	fixture.stop()
}

func TestWorkflowNotificationReconcilesCachedChildInboxOnce(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childModelFixture(t)
	dispatcher := newWorkflowDispatch(t.Context(), fixture.base.runtime,
		fixture.engine.options.GenerationID, nil)
	parent, err := dispatcher.child.resolve(t.Context(), input)
	require.NoError(t, err)
	inbox := parent.actor.run.inbox
	_, arrived := inbox.arrival()
	_, err = fixture.base.store.AcceptChildExecutionMessage(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID,
		session.AcceptExecutionMessageInput{
			RequestID: uuid.New(), WorkingDirectory: fixture.base.workspace,
			Message: "durable incoming child report", Capacity: 1,
		})
	require.NoError(t, err)
	count, _ := inbox.arrival()
	assert.Zero(t, count)
	require.NoError(t, dispatcher.notifyParent(t.Context(), input.Execution.SessionID,
		input.Execution.ExecutionID))
	count, next := inbox.arrival()
	assert.Equal(t, 1, count)
	select {
	case <-arrived:
	default:
		t.Fatal("durable row did not signal cached child")
	}
	require.NoError(t, dispatcher.notifyParent(t.Context(), input.Execution.SessionID,
		input.Execution.ExecutionID))
	_, after := inbox.arrival()
	assert.Equal(t, next, after)
	select {
	case <-next:
		t.Fatal("duplicate child arrival signal")
	default:
	}
}

func TestWorkflowNotificationDoesNotConstructUncachedChild(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childModelFixture(t)
	dispatcher := newWorkflowDispatch(t.Context(), fixture.base.runtime,
		fixture.engine.options.GenerationID, nil)
	before, err := fixture.base.store.GetExecution(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID)
	require.NoError(t, err)
	require.NoError(t, dispatcher.notifyParent(t.Context(), input.Execution.SessionID,
		input.Execution.ExecutionID))
	after, err := fixture.base.store.GetExecution(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.Empty(t, dispatcher.child.slots)
	registry, err := fixture.base.runtime.sessionAgentRuns(input.Execution.SessionID)
	require.NoError(t, err)
	assert.Empty(t, registry.List())
}

func TestWorkflowNotificationRefusesSupersededRootHolder(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childModelFixture(t, elelemtest.Text("stored report"))
	dispatcher := newWorkflowDispatch(t.Context(), fixture.base.runtime,
		fixture.engine.options.GenerationID, nil)
	dispatcher.bind(&fixture.engine.options)
	parentInput := fixture.input
	parent, err := dispatcher.root.resolve(t.Context(), parentInput)
	require.NoError(t, err)
	t.Cleanup(func() {
		fixture.base.runtime.closeActiveUserMessageQueue(parent.prepared)
	})
	_, arrived := parent.prepared.userMessages.arrival()
	_, err = fixture.base.store.RestoreExecutionTurn(t.Context(), parentInput.SessionID,
		parentInput.ExecutionID, session.RestoreExecutionTurnInput{
			OwnerGenerationID: uuid.New(), PreviousLeaseID: parent.lease.ID,
		})
	require.NoError(t, err)
	fixture.input = input.Execution
	fixture.input.MaxConcurrentTools = 1
	instance := startCompactingWorkflow(t, fixture)
	_, err = client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, 10*time.Second)
	require.NoError(t, err)
	count, next := parent.prepared.userMessages.arrival()
	assert.Zero(t, count)
	assert.Equal(t, arrived, next)
	select {
	case <-arrived:
		t.Fatal("report signalled superseded root holder")
	default:
	}
	fixture.stop()
}

func TestWorkflowNotificationDoesNotSignalCancelledChild(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childModelFixture(t)
	dispatcher := newWorkflowDispatch(t.Context(), fixture.base.runtime,
		fixture.engine.options.GenerationID, nil)
	parent, err := dispatcher.child.resolve(t.Context(), input)
	require.NoError(t, err)
	_, arrived := parent.actor.run.inbox.arrival()
	registry, err := fixture.base.runtime.sessionAgentRuns(input.Execution.SessionID)
	require.NoError(t, err)
	registry.Cancel(parent.actor.run.ID)
	_, err = fixture.base.store.AcceptChildExecutionMessage(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID,
		session.AcceptExecutionMessageInput{
			RequestID: uuid.New(), WorkingDirectory: fixture.base.workspace,
			Message: "retain while parent cancelled", Capacity: 1,
		})
	require.NoError(t, err)
	require.NoError(t, dispatcher.notifyParent(t.Context(), input.Execution.SessionID,
		input.Execution.ExecutionID))
	count, next := parent.actor.run.inbox.arrival()
	assert.Zero(t, count)
	assert.Equal(t, arrived, next)
	select {
	case <-arrived:
		t.Fatal("notification signalled cancelled child lifetime")
	default:
	}
}

func TestWorkflowNotificationRequiresExecutionStorage(t *testing.T) {
	t.Parallel()
	dispatcher := newWorkflowDispatch(t.Context(), &Runtime{}, uuid.New(), nil)
	require.ErrorIs(t, dispatcher.notifyParent(t.Context(), uuid.New(), uuid.New()),
		commerr.ErrRequiredFieldNotSet)
}
