package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cschleiden/go-workflows/backend/history"
	"github.com/cschleiden/go-workflows/client"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const preparedWorkflowTestPrompt = "Refreshed example instructions."

func TestExecutionWorkflowModelRetryReusesPreparedInput(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Turn{Err: commerr.ErrFetchFailed}, elelemtest.Text("done"),
	)
	fixture := newExecutionLoopFixture(t, driver, true)
	var calls atomic.Int32
	fixture.engine.options.PrepareRound = func(
		_ context.Context, input workflowRoundInput,
	) (json.RawMessage, error) {
		calls.Add(1)
		var conversation workflowConversation
		if err := json.Unmarshal(input.Execution.Context, &conversation); err != nil {
			return nil, ctxerrors.Wrap(err, "decode preparation test context")
		}

		conversation.SystemPrompt = preparedWorkflowTestPrompt
		encoded, err := json.Marshal(conversation)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "encode preparation test context")
		}

		return encoded, nil
	}
	instance := fixture.start(t)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)
	assert.EqualValues(t, 1, calls.Load())
	requests := driver.Requests()
	require.Len(t, requests, 2)
	assert.Equal(t, preparedWorkflowTestPrompt, requests[0].Messages[0].Text())
	assert.Equal(t, requests[0].Messages, requests[1].Messages)
	fixture.stop()
}

func TestExecutionWorkflowPreparationSurvivesReopen(t *testing.T) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), true)
	input := workflowRoundInput{Execution: fixture.input}
	prepared, err := fixture.engine.prepareRound(t.Context(), input)
	require.NoError(t, err)
	require.NoError(t, fixture.base.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{
		Directory: fixture.base.stateDirectory,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)
	fixture.engine.options.Store = store
	fixture.engine.options.GenerationID = uuid.New()
	fixture.engine.options.PrepareRound = func(
		context.Context, workflowRoundInput,
	) (json.RawMessage, error) {
		t.Error("completed preparation ran again")

		return json.RawMessage(`{}`), nil
	}
	reused, err := fixture.engine.prepareRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	assert.JSONEq(t, string(prepared.Output), string(reused.Output))
}

func TestExecutionWorkflowInvalidPreparationReleasesClaim(t *testing.T) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), true)
	fixture.engine.options.PrepareRound = func(
		context.Context, workflowRoundInput,
	) (json.RawMessage, error) {
		return json.RawMessage(`[]`), nil
	}
	_, err := fixture.engine.prepareRound(t.Context(),
		workflowRoundInput{Execution: fixture.input})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	page, err := fixture.base.store.ListExecutionActivities(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.ActivityStateFailed, page.Items[0].State)
}

func TestExecutionWorkflowPausePreventsPreparation(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("done"))
	fixture := newExecutionLoopFixture(t, driver, true)
	var calls atomic.Int32
	fixture.engine.options.PrepareRound = func(
		_ context.Context, input workflowRoundInput,
	) (json.RawMessage, error) {
		calls.Add(1)

		return input.Execution.Context, nil
	}
	require.NoError(t, fixture.engine.pause(t.Context(), fixture.input.SessionID,
		fixture.input.ExecutionID, "leave preparation paused"))
	instance := fixture.start(t)
	require.Eventually(t, func() bool {
		records, err := fixture.storage.GetWorkflowInstanceHistory(
			t.Context(), instance, nil)
		if err != nil {
			return false
		}

		for _, event := range records {
			if event.Type == history.EventType_ActivityCompleted {
				return true
			}
		}

		return false
	}, 10*time.Second, 10*time.Millisecond)
	assert.Zero(t, calls.Load())
	assert.Zero(t, driver.Calls())
	_, err := fixture.base.store.ResumeExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	require.NoError(t, fixture.api.SignalWorkflow(t.Context(),
		fmt.Sprintf("%s/%s", fixture.input.SessionID, fixture.input.ExecutionID),
		workflowResumeSignal, struct{}{}))
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)
	assert.EqualValues(t, 1, calls.Load())
	assert.Equal(t, 1, driver.Calls())
	fixture.stop()
}
