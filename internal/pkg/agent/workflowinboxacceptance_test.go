package agent

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/cschleiden/go-workflows/client"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/essessey"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowQueueAcceptsDistinctIDsAndDeliversWithoutMemoryDrain(
	t *testing.T,
) {
	t.Parallel()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver(elelemtest.Text("continued")))
	input := TurnRequest{RequestID: uuid.New(), Message: "same queued text"}
	require.NoError(t, prepared.userMessages.enqueue(t.Context(), input))
	require.NoError(t, prepared.userMessages.enqueue(t.Context(), input))
	input.RequestID = uuid.New()
	require.NoError(t, prepared.userMessages.enqueue(t.Context(), input))
	count, _ := prepared.userMessages.arrival()
	assert.Equal(t, 2, count)
	assert.Zero(t, prepared.userMessages.queue.Len())
	result, err := fixture.engine.prepareRound(t.Context(), workflowRoundInput{
		Execution: fixture.input,
	})
	require.NoError(t, err)
	inputContext := fixture.input
	inputContext.Context = result.Output
	_, err = fixture.engine.modelRound(t.Context(), workflowRoundInput{
		Execution: inputContext,
	})
	require.NoError(t, err)
	count, _ = prepared.userMessages.arrival()
	assert.Zero(t, count)
	page, err := fixture.base.store.ListExecutionMessages(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 2)
	messages, err := fixture.base.store.ListMessages(t.Context(),
		fixture.input.SessionID, session.ListMessagesOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{
		prepared.input.Message, input.Message,
		input.Message, "continued",
	}, messageContents(messages.Items))
}

func TestWorkflowQueueRepeatedRequestKeepsFrozenSkillsAndDirectory(
	t *testing.T,
) {
	t.Parallel()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	input := TurnRequest{RequestID: uuid.New(), Message: "use :core:planning"}
	queue := prepared.userMessages
	require.NoError(t, queue.enqueue(t.Context(), input))
	before, err := fixture.base.store.ListExecutionMessages(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, before.Items, 1)
	var parts []session.ExecutionInboxPart
	require.NoError(t, json.Unmarshal([]byte(before.Items[0].PartsJSON), &parts))
	require.NotEmpty(t, parts)
	queue.mutex.Lock()
	queue.workspace = fixture.base.otherWorkspace
	queue.mutex.Unlock()
	require.NoError(t, queue.enqueue(t.Context(), input))
	after, err := fixture.base.store.ListExecutionMessages(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	assert.Equal(t, before.Items, after.Items)
	input.Message = "different text"
	require.ErrorIs(t, queue.enqueue(t.Context(), input), commerr.ErrConflict)
}

func TestWorkflowQueueCallbackCanSynchronouslyAcceptAnotherMessage(
	t *testing.T,
) {
	t.Parallel()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	first := TurnRequest{RequestID: uuid.New(), Message: "first request"}
	second := TurnRequest{RequestID: uuid.New(), Message: "callback request"}
	prepared.turn.sink = func(event Event) error {
		if event.Type == EventTypeUserMessageCreated &&
			event.RequestID == first.RequestID {
			return prepared.userMessages.enqueue(t.Context(), second)
		}

		return nil
	}
	require.NoError(t, prepared.userMessages.enqueue(t.Context(), first))
	page, err := fixture.base.store.ListExecutionMessages(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.Equal(t, first.RequestID, page.Items[0].RequestID)
	assert.Equal(t, second.RequestID, page.Items[1].RequestID)
}

func TestWorkflowQueueGeneratesDistinctMissingRequestIDs(t *testing.T) {
	t.Parallel()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	input := TurnRequest{Message: "same text without an explicit ID"}
	require.NoError(t, prepared.userMessages.enqueue(t.Context(), input))
	require.NoError(t, prepared.userMessages.enqueue(t.Context(), input))
	page, err := fixture.base.store.ListExecutionMessages(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.NotEqual(t, uuid.Nil, page.Items[0].RequestID)
	assert.NotEqual(t, page.Items[0].RequestID, page.Items[1].RequestID)
}

func TestWorkflowQueueUnknownSkillLeavesNoAcceptance(t *testing.T) {
	t.Parallel()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	err := prepared.userMessages.enqueue(t.Context(), TurnRequest{
		RequestID: uuid.New(), Message: "use :missing-skill",
	})
	require.ErrorIs(t, err, harness.ErrSkillNotFound)
	page, err := fixture.base.store.ListExecutionMessages(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	count, _ := prepared.userMessages.arrival()
	assert.Zero(t, count)
}

func TestWorkflowQueueRefusesClosedPausedAndFullAcceptance(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		setup   func(*testing.T, executionLoopFixture, *preparedTurn)
		wantErr error
		rows    int
	}{
		{
			name: "closed", wantErr: commerr.ErrConflict,
			setup: func(t *testing.T, _ executionLoopFixture, p *preparedTurn) {
				t.Helper()
				p.userMessages.close()
			},
		},
		{
			name: "paused", wantErr: commerr.ErrInvalidState,
			setup: func(t *testing.T, f executionLoopFixture, _ *preparedTurn) {
				t.Helper()
				_, err := f.base.store.PauseExecution(t.Context(), f.input.SessionID,
					f.input.ExecutionID, "retain this work")
				require.NoError(t, err)
			},
		},
		{
			name: "full", wantErr: commerr.ErrConflict, rows: 1,
			setup: func(t *testing.T, _ executionLoopFixture, p *preparedTurn) {
				t.Helper()
				p.userMessages.capacity = 1
				require.NoError(t, p.userMessages.enqueue(t.Context(), TurnRequest{
					RequestID: uuid.New(), Message: "fills the only slot",
				}))
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, prepared := newRootModelWorkflowFixture(t,
				elelemtest.NewScriptedDriver())
			tc.setup(t, fixture, prepared)
			err := prepared.userMessages.enqueue(t.Context(), TurnRequest{
				RequestID: uuid.New(), Message: "refused request",
			})
			require.ErrorIs(t, err, tc.wantErr)
			page, err := fixture.base.store.ListExecutionMessages(t.Context(),
				fixture.input.SessionID, fixture.input.ExecutionID,
				session.ListExecutionInboxOptions{})
			require.NoError(t, err)
			assert.Len(t, page.Items, tc.rows)
		})
	}
}

func TestWorkflowQueueConcurrentRepeatedRequest(t *testing.T) {
	t.Parallel()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	input := TurnRequest{RequestID: uuid.New(), Message: "same accepted request"}
	var group sync.WaitGroup

	for range 8 {
		group.Go(func() {
			assert.NoError(t, prepared.userMessages.enqueue(t.Context(), input))
		})
	}

	group.Wait()
	page, err := fixture.base.store.ListExecutionMessages(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
	count, _ := prepared.userMessages.arrival()
	assert.Equal(t, 1, count)
}

func TestWorkflowQueueMessageDuringFinalTextContinuesWorkflow(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("first answer"),
		elelemtest.Text("answer with the queued request"))
	fixture, prepared := newRootModelWorkflowFixture(t, driver)
	accepted := false
	prepared.turn.sink = func(event Event) error {
		if !accepted && event.Type == essessey.EventTypeContentBlockDelta {
			accepted = true

			return prepared.userMessages.enqueue(t.Context(), TurnRequest{
				RequestID: uuid.New(), Message: "also inspect the pending work",
			})
		}

		return nil
	}
	instance := fixture.start(t)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "answer with the queued request", result.Text)
	requests := driver.Requests()
	assert.Len(t, requests, 2)
	page, err := fixture.base.store.ListExecutionMessages(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.InboxMessageStateDelivered, page.Items[0].State)
	fixture.stop()
}

func TestWorkflowQueueLateMessageKeepsRoundBudget(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("last permitted answer"),
		elelemtest.Text("must not request another round"))
	fixture, prepared := newRootModelWorkflowFixture(t, driver)
	fixture.input.MaxRounds = 1
	accepted := false
	prepared.turn.sink = func(event Event) error {
		if !accepted && event.Type == essessey.EventTypeContentBlockDelta {
			accepted = true

			return prepared.userMessages.enqueue(t.Context(), TurnRequest{
				RequestID: uuid.New(), Message: "retain the unfinished request",
			})
		}

		return nil
	}
	instance := fixture.start(t)
	_, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, 10*time.Second)
	require.Error(t, err)
	assert.Equal(t, 1, driver.Calls())
	page, err := fixture.base.store.ListExecutionMessages(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.InboxMessageStateQueued, page.Items[0].State)
	execution, err := fixture.base.store.GetExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateFailed, execution.State)
	fixture.stop()
}
