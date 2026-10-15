package agent

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cschleiden/go-workflows/client"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowFinalDecisionReusesContinuationAfterReopen(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("saved answer"))
	fixture, prepared := newRootModelWorkflowFixture(t, driver)
	request := workflowRoundInput{Execution: fixture.input}
	model, err := fixture.engine.modelRound(t.Context(), request)
	require.NoError(t, err)
	require.NoError(t, prepared.userMessages.enqueue(t.Context(), TurnRequest{
		RequestID: uuid.New(), Message: "retain continuation",
	}))
	input := workflowFinishInput{
		Execution: fixture.input,
		Result: workflowFinishResult{
			Result: model.Output, AllowContinuation: true,
			Finish: session.FinishExecutionInput{
				State: models.ExecutionStateCompleted,
			},
		},
	}
	first, err := fixture.engine.finishExecution(t.Context(), input)
	require.NoError(t, err)
	assert.True(t, first.Output.Continue)
	_, err = fixture.base.store.PauseExecution(t.Context(), fixture.input.SessionID,
		fixture.input.ExecutionID, "retain final decision")
	require.NoError(t, err)
	fixture.stop()
	require.NoError(t, fixture.storage.Close())
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
	fixture.engine.options.Lifecycle = t.Context()
	reused, err := fixture.engine.finishExecution(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionPaused, reused.Decision)
	assert.Equal(t, 1, driver.Calls())
	execution, err := store.GetExecution(t.Context(), fixture.input.SessionID,
		fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, execution.State)
	_, err = store.ResumeExecution(t.Context(), fixture.input.SessionID,
		fixture.input.ExecutionID)
	require.NoError(t, err)
	reused, err = fixture.engine.finishExecution(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	assert.Equal(t, first.Output, reused.Output)
	assert.Equal(t, 1, driver.Calls())
}

func TestWorkflowFinalDecisionRejectsNegativeRound(t *testing.T) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), true)
	_, err := fixture.engine.finishExecution(t.Context(), workflowFinishInput{
		Execution: fixture.input, Round: -1,
	})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

func TestWorkflowFinishCleanupFailureReusesCommittedResult(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		panic bool
	}{
		{name: "error"}, {name: "panic", panic: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), true)
			var calls atomic.Int32
			fixture.engine.options.AfterFinish = func(context.Context,
				workflowFinishInput, workflowFinishResult,
			) error {
				if calls.Add(1) == 1 {
					if tc.panic {
						panic("example cleanup panic")
					}

					return ctxerrors.Wrap(commerr.ErrFetchFailed,
						"example cleanup failure")
				}

				return nil
			}
			input := callbackFinishInput(fixture)
			_, err := fixture.engine.finishExecution(t.Context(), input)
			require.Error(t, err)
			execution, err := fixture.base.store.GetExecution(t.Context(),
				fixture.input.SessionID, fixture.input.ExecutionID)
			require.NoError(t, err)
			assert.Equal(t, models.ExecutionStateCompleted, execution.State)
			reused, err := fixture.engine.finishExecution(t.Context(), input)
			require.NoError(t, err)
			assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
			assert.EqualValues(t, 2, calls.Load())
		})
	}
}

type finishAcceptanceStore struct {
	session.ExecutionStorage
	beforeFinish func(context.Context) error
}

func (s *finishAcceptanceStore) BeginExecutionActivity(
	ctx context.Context,
	sessionID, executionID, generationID uuid.UUID,
	input session.ScheduleActivityInput,
) (*session.ActivityClaim, error) {
	if input.Name == session.ExecutionFinishActivityName {
		if err := s.beforeFinish(ctx); err != nil {
			return nil, ctxerrors.Wrap(err, "accept input before final claim")
		}
	}

	return s.ExecutionStorage.BeginExecutionActivity(ctx, sessionID,
		executionID, generationID, input)
}

func TestWorkflowFinishWaitsForExplicitResume(t *testing.T) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), true)
	input := workflowFinishInput{
		Execution: fixture.input,
		Result: workflowFinishResult{
			Result: workflowRoundResult{Context: fixture.input.Context},
			Finish: session.FinishExecutionInput{
				State: models.ExecutionStateCompleted,
			},
		},
	}
	require.NoError(t, fixture.engine.pause(t.Context(), fixture.input.SessionID,
		fixture.input.ExecutionID, "leave completed model paused"))
	result, err := fixture.engine.finishExecution(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionPaused, result.Decision)
	_, err = fixture.base.store.ResumeExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	result, err = fixture.engine.finishExecution(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionExecute, result.Decision)
	result, err = fixture.engine.finishExecution(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, result.Decision)
}

func TestWorkflowRoundExhaustionRecordsFailureAndContext(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.ToolCall(
		runtimeToolCallID, toolNameReadFile,
		runtimeToolArguments(t, runtimeToolFileName)))
	fixture := newExecutionLoopFixture(t, driver, true)
	fixture.input.MaxRounds = 1
	instance := fixture.start(t)
	_, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, 10*time.Second)
	require.Error(t, err)
	execution, err := fixture.base.store.GetExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateFailed, execution.State)
	assert.NotEmpty(t, execution.FailureDetail)
	assert.NotEqual(t, string(fixture.input.Context), execution.ContextJSON)
	assert.Equal(t, 1, driver.Calls())
	fixture.stop()
}

func TestWorkflowRoundExhaustionDoesNotExecutePendingTools(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.ToolCall(
		runtimeToolCallID, toolNameReadFile, `{}`))
	fixture := newExecutionLoopFixture(t, driver, true)
	fixture.input.MaxRounds = 1
	var effects atomic.Int32
	fixture.engine.options.ToolCall = func(context.Context, workflowToolInput) (
		workflowToolResult, error,
	) {
		effects.Add(1)

		return workflowToolResult{Result: elelem.ToolResult{
			Content: "effect completed",
		}}, nil
	}
	instance := fixture.start(t)
	_, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, 10*time.Second)
	require.Error(t, err)
	assert.Zero(t, effects.Load())
	fixture.stop()
}

func TestWorkflowModelFailureRecordsLastContext(t *testing.T) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), true)
	prepared := json.RawMessage(`{"messages":["refreshed context"]}`)
	fixture.engine.options.PrepareRound = func(
		context.Context, workflowRoundInput,
	) (json.RawMessage, error) {
		return prepared, nil
	}
	fixture.engine.options.ModelRound = func(
		context.Context, workflowRoundInput,
	) (workflowRoundResult, error) {
		return workflowRoundResult{}, ctxerrors.Wrap(
			commerr.ErrFetchFailed, "example provider unavailable")
	}
	instance := fixture.start(t)
	_, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, 10*time.Second)
	require.Error(t, err)
	execution, err := fixture.base.store.GetExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateFailed, execution.State)
	assert.JSONEq(t, string(prepared), execution.ContextJSON)
	assert.True(t, json.Valid([]byte(execution.ContextJSON)))
	fixture.stop()
}

type workflowFinishGateStore struct {
	session.ExecutionStorage
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (s *workflowFinishGateStore) CompleteExecutionActivity(
	ctx context.Context,
	sessionID uuid.UUID,
	lease session.ActivityLease,
	input session.CompleteActivityInput,
) (*models.ExecutionActivity, error) {
	if input.Finish != nil {
		s.once.Do(func() {
			close(s.entered)
			<-s.release
		})
	}

	return s.ExecutionStorage.CompleteExecutionActivity(ctx,
		sessionID, lease, input)
}

func TestWorkflowPauseAfterFinalModelDoesNotRepeatProvider(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text(runtimeToolFinalText))
	fixture := newExecutionLoopFixture(t, driver, true)
	gate := &workflowFinishGateStore{
		ExecutionStorage: fixture.base.store,
		entered:          make(chan struct{}), release: make(chan struct{}),
	}
	release := sync.OnceFunc(func() { close(gate.release) })
	t.Cleanup(release)

	fixture.engine.options.Store = gate
	instance := fixture.start(t)
	select {
	case <-gate.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("final activity did not reach completion gate")
	}
	require.NoError(t, fixture.engine.pause(t.Context(), fixture.input.SessionID,
		fixture.input.ExecutionID, "leave final model paused"))
	release()
	require.Eventually(t, func() bool {
		page, err := fixture.base.store.ListExecutionActivities(t.Context(),
			fixture.input.SessionID, fixture.input.ExecutionID,
			session.ListEventsOptions{})

		return err == nil && len(page.Items) == 5 &&
			page.Items[4].State == models.ActivityStatePaused
	}, 10*time.Second, 10*time.Millisecond)
	assert.Equal(t, 1, driver.Calls())
	_, err := fixture.base.store.ResumeExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	require.NoError(t, fixture.api.SignalWorkflow(t.Context(), instance.InstanceID,
		workflowResumeSignal, struct{}{}))
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, runtimeToolFinalText, result.Text)
	assert.Equal(t, 1, driver.Calls())
	fixture.stop()
}
