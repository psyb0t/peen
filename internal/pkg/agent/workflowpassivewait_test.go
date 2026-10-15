package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPassiveWaitHandlerRetriesAfterInterruption(t *testing.T) {
	t.Parallel()
	fixture, _, _ := rootModelProjectionFixture(t,
		elelemtest.ToolCall("retainedWait", toolNameWaitMessages,
			`{"timeout":"1h"}`))
	round := workflowRoundInput{Execution: fixture.input}
	model, err := fixture.engine.modelRound(t.Context(), round)
	require.NoError(t, err)
	round.Execution.Context = model.Output.Context
	input := workflowToolPhaseInput{
		Tool:  workflowToolInput{Round: round, Call: model.Output.Calls[0]},
		Phase: workflowToolPhasePre,
	}
	pre, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	input.Phase, input.State = workflowToolPhaseHandler, pre.Output
	fixture.engine.options.ToolPhase = func(context.Context,
		workflowToolPhaseInput,
	) (workflowToolPhaseState, error) {
		return input.State, context.Canceled
	}
	_, err = fixture.engine.toolPhase(t.Context(), input)
	require.ErrorIs(t, err, context.Canceled)
	page, err := fixture.base.store.ListExecutionActivities(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	handlerCount := 0
	for _, activity := range page.Items {
		if activity.StepID == "tool:0:0:handler" {
			handlerCount++
			assert.Equal(t, models.ActivityReplayPolicyRetry, activity.ReplayPolicy)
		}
	}
	assert.Equal(t, 1, handlerCount)
}

func TestExecutionHistoryHandlerRetriesAfterInterruption(t *testing.T) {
	t.Parallel()
	for _, name := range []string{toolNameSearchExecutions, toolNameReadExecution} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), false)
			input := toolPhaseTestInput(fixture)
			input.Phase, input.State.LastPhase = workflowToolPhaseHandler, workflowToolPhasePre
			input.Tool.Call.Name = name
			fixture.engine.options.ToolPhase = func(context.Context, workflowToolPhaseInput) (workflowToolPhaseState, error) {
				return input.State, context.Canceled
			}
			_, err := fixture.engine.toolPhase(t.Context(), input)
			require.ErrorIs(t, err, context.Canceled)
			page, err := fixture.base.store.ListExecutionActivities(t.Context(), fixture.input.SessionID, fixture.input.ExecutionID, session.ListEventsOptions{})
			require.NoError(t, err)
			require.Len(t, page.Items, 1)
			assert.Equal(t, models.ActivityReplayPolicyRetry, page.Items[0].ReplayPolicy)
			assert.Equal(t, models.ActivityStateFailed, page.Items[0].State)
			saved := page.Items[0]
			claim, err := fixture.base.store.BeginExecutionActivity(t.Context(), fixture.input.SessionID,
				fixture.input.ExecutionID, fixture.engine.options.GenerationID, session.ScheduleActivityInput{
					StepID: saved.StepID, Kind: saved.Kind, ReplayPolicy: saved.ReplayPolicy,
					Name: saved.Name, Input: json.RawMessage(saved.InputJSON),
				})
			require.NoError(t, err)
			assert.Equal(t, session.ActivityDecisionExecute, claim.Decision)
			assert.Equal(t, saved.Attempt+1, claim.Lease.Attempt)
			input.Phase = workflowToolPhasePost
			assert.Equal(t, models.ActivityReplayPolicyReconcile, workflowToolPhaseReplayPolicy(input))
		})
	}
}

func TestPassiveWaitPreparationRetainsTimeout(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		arguments string
		prepared  bool
		timeout   time.Duration
	}{
		{
			name: "finite", arguments: `{"timeout":"1h"}`,
			prepared: true, timeout: time.Hour,
		},
		{name: "unbounded", arguments: `{}`, prepared: true},
		{name: "negative", arguments: `{"timeout":"-1s"}`},
		{name: "zero", arguments: `{"timeout":"0s"}`},
		{name: "malformed", arguments: `{"timeout":3}`},
		{name: "invalid duration", arguments: `{"timeout":"later"}`},
		{name: "unknown field", arguments: `{"extra":true}`},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := phaseExecutionTestInput(t)
			input.Tool.Call.Name = toolNameWaitMessages
			input.Tool.Call.Arguments = json.RawMessage(tc.arguments)
			state := workflowToolPhaseState{
				Event: &workflowToolEventState{
					RawArguments: json.RawMessage(tc.arguments),
				},
			}
			now := time.Now().UTC()
			retainWorkflowPassiveWait(input, &state, now)
			if !tc.prepared {
				assert.Nil(t, state.Wait)

				return
			}
			require.NotNil(t, state.Wait)
			assert.NotEqual(t, uuid.Nil, state.Wait.ID)
			if tc.timeout == 0 {
				assert.True(t, state.Wait.Deadline.IsZero())
			} else {
				assert.Equal(t, now.Add(tc.timeout), state.Wait.Deadline)
			}
			encoded, err := json.Marshal(state)
			require.NoError(t, err)
			var restored workflowToolPhaseState
			require.NoError(t, json.Unmarshal(encoded, &restored))
			assert.Equal(t, state.Wait, restored.Wait)
		})
	}
}

func TestPassiveWaitRetainsOriginalHandlerArguments(t *testing.T) {
	t.Parallel()
	input := phaseExecutionTestInput(t)
	input.Tool.Call.Name = toolNameWaitMessages
	input.Tool.Call.Arguments = json.RawMessage(`{"timeout":"1h"}`)
	got := waitMessagesInput{}
	tool := elelem.Tool{
		Name:            toolNameWaitMessages,
		ArgumentsSchema: json.RawMessage(waitMessagesSchema),
		PreRun: func(_ context.Context, event *elelem.ToolEvent) error {
			event.RawArguments = json.RawMessage(`{"timeout":"2h"}`)

			return nil
		},
		Handler: hostToolHandler(func(_ context.Context, value waitMessagesInput) (
			waitMessagesOutput, error,
		) {
			got = value

			return waitMessagesOutput{}, nil
		}),
	}
	set := elelem.NewToolSet(tool)
	before := time.Now().UTC()
	pre, err := runWorkflowToolPhase(t.Context(), set, input,
		workflowPhaseExecutionOptions{})
	require.NoError(t, err)
	require.NotNil(t, pre.Wait)
	assert.False(t, pre.Wait.Deadline.Before(before.Add(time.Hour)))
	assert.False(t, pre.Wait.Deadline.After(time.Now().UTC().Add(time.Hour)))
	assert.JSONEq(t, `{"timeout":"2h"}`, string(pre.Event.RawArguments))
	input.State, input.Phase = pre, workflowToolPhaseHandler
	_, err = runWorkflowToolPhase(t.Context(), set, input,
		workflowPhaseExecutionOptions{})
	require.NoError(t, err)
	assert.Equal(t, "1h", got.Timeout)
}

func TestPassiveWaitIdentityControlsRetryPolicy(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		tool    string
		phase   workflowToolPhase
		missing bool
		forged  bool
		wantErr error
		policy  models.ActivityReplayPolicy
	}{
		{
			name: "handler", tool: toolNameWaitMessages,
			phase: workflowToolPhaseHandler, policy: models.ActivityReplayPolicyRetry,
		},
		{
			name: "legacy", tool: toolNameWaitMessages, missing: true,
			phase: workflowToolPhaseHandler, policy: models.ActivityReplayPolicyReconcile,
		},
		{
			name: "hook", tool: toolNameWaitMessages,
			phase: workflowToolPhaseOutcome, policy: models.ActivityReplayPolicyReconcile,
		},
		{
			name: "forged", tool: toolNameWaitMessages, forged: true,
			phase: workflowToolPhaseHandler, wantErr: commerr.ErrValidationFailed,
			policy: models.ActivityReplayPolicyReconcile,
		},
		{
			name: "ask", tool: toolNameAsk,
			phase: workflowToolPhaseHandler, wantErr: commerr.ErrValidationFailed,
			policy: models.ActivityReplayPolicyReconcile,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := phaseExecutionTestInput(t)
			input.Tool.Call.Name, input.Phase = tc.tool, tc.phase
			input.State.LastPhase = workflowToolPhasePre
			if tc.phase == workflowToolPhaseOutcome {
				input.State.LastPhase = workflowToolPhaseHandler
			}
			if !tc.missing {
				input.State.Wait = &workflowPassiveWait{
					ID: workflowPassiveWaitID(input.Tool),
				}
			}
			if tc.forged {
				input.State.Wait.ID = uuid.New()
			}
			require.ErrorIs(t, input.validate(), tc.wantErr)
			assert.Equal(t, tc.policy, workflowToolPhaseReplayPolicy(input))
		})
	}
}

func TestPassiveWaitUsesSavedDeadline(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		unbounded bool
		queued    int
		cancel    bool
		want      waitMessagesOutput
		wantErr   error
	}{
		{name: "expired", want: waitMessagesOutput{TimedOut: true}},
		{name: "queued", queued: 2, want: waitMessagesOutput{Waiting: 2}},
		{
			name: "unbounded queued", unbounded: true, queued: 1,
			want: waitMessagesOutput{Waiting: 1},
		},
		{
			name: "unbounded cancelled", unbounded: true, cancel: true,
			wantErr: context.Canceled,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wait := workflowPassiveWait{ID: uuid.New()}
			if !tc.unbounded {
				wait.Deadline = time.Now().UTC().Add(-time.Hour)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			ctx = context.WithValue(ctx, workflowPassiveWaitContextKey{}, wait)
			if tc.cancel {
				cancel()
			}
			arrival := func() (int, <-chan struct{}) { return tc.queued, nil }
			got, err := waitForMessages(ctx, time.Hour, arrival, nil, noNotices)
			require.ErrorIs(t, err, tc.wantErr)
			assert.Equal(t, tc.want, got)
		})
	}
}

func passiveRootWaitFixture(t *testing.T) (
	executionLoopFixture, workflowRoundInput,
) {
	t.Helper()
	fixture, _, _ := rootModelProjectionFixture(t,
		elelemtest.ToolCall("rootWait", toolNameWaitMessages,
			`{"timeout":"1ns"}`))

	return fixture, workflowRoundInput{Execution: fixture.input}
}

func passiveChildWaitFixture(t *testing.T) (
	executionLoopFixture, workflowRoundInput,
) {
	t.Helper()
	fixture, input, _, _ := childModelProjectionFixture(t, nil,
		elelemtest.ToolCall("childWait", toolNameWaitMessages,
			`{"timeout":"1ns"}`))
	input.Execution.MaxConcurrentTools = 1

	return fixture, input
}

func TestPassiveWaitPhaseAdaptersRetainDeadlineAfterSQLiteReopen(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		fixture func(*testing.T) (executionLoopFixture, workflowRoundInput)
	}{
		{name: "root", fixture: passiveRootWaitFixture},
		{name: "child", fixture: passiveChildWaitFixture},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, round := tc.fixture(t)
			model, err := fixture.engine.modelRound(t.Context(), round)
			require.NoError(t, err)
			round.Execution.Context = model.Output.Context
			input := workflowToolPhaseInput{
				Tool:  workflowToolInput{Round: round, Call: model.Output.Calls[0]},
				Phase: workflowToolPhasePre,
			}
			pre, err := fixture.engine.toolPhase(t.Context(), input)
			require.NoError(t, err)
			require.NotNil(t, pre.Output.Wait)
			fixture.engine.options.GenerationID = uuid.New()
			store, replacement := reopenModelProjectionRuntime(t, fixture, round)
			fixture.engine.options.Store = store
			dispatch := newWorkflowDispatch(t.Context(), replacement,
				fixture.engine.options.GenerationID, nil)
			dispatch.bind(&fixture.engine.options)
			input.State = pre.Output
			for _, phase := range []workflowToolPhase{
				workflowToolPhaseHandler, workflowToolPhaseOutcome,
				workflowToolPhasePost, workflowToolPhaseProject,
			} {
				input.Phase = phase
				result, err := fixture.engine.toolPhase(t.Context(), input)
				require.NoError(t, err)
				assert.Equal(t, pre.Output.Wait, result.Output.Wait)
				input.State = result.Output
				input.Tool.Round.Execution.Context = result.Output.Outcome.Context
			}
			var result waitMessagesOutput
			require.NoError(t, json.Unmarshal(
				[]byte(input.State.Outcome.Result.Content), &result))
			assert.True(t, result.TimedOut)
		})
	}
}
