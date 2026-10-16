package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cschleiden/go-workflows/client"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/hooks"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assertPostCompactionContext(t *testing.T, fixture executionLoopFixture, expected json.RawMessage) {
	t.Helper()
	page, err := fixture.base.store.ListExecutionActivities(t.Context(), fixture.input.SessionID, fixture.input.ExecutionID, session.ListEventsOptions{})
	require.NoError(t, err)
	var saved *models.ExecutionActivity
	for _, activity := range page.Items {
		if activity.Name != compactionHookActivityName {
			continue
		}
		var input workflowCompactionHookInput
		require.NoError(t, json.Unmarshal([]byte(activity.InputJSON), &input))
		if input.Event == harness.HookEventPostCompact {
			require.Nil(t, saved)
			saved = activity
		}
	}
	require.NotNil(t, saved)
	assert.Equal(t, hash(string(expected)), hash(saved.ContextJSON))
}

func TestWorkflowCompactionHooksRunThroughSDK(t *testing.T) {
	t.Parallel()
	fixture, driver := compactionHookFixture(t, false, elelemtest.Text("SDK summary"), elelemtest.Text("continued answer"))
	var events []harness.HookEvent
	fixture.base.runtime.hookCallback = func(_ context.Context, input hooks.Invocation) (hooks.Outcome, error) {
		if input.Event == harness.HookEventPreCompact || input.Event == harness.HookEventPostCompact {
			events = append(events, input.Event)
		}

		return hooks.Outcome{}, nil
	}
	instance := startCompactingWorkflow(t, fixture)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(), fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, "continued answer", result.Text)
	assert.Equal(t, []harness.HookEvent{harness.HookEventPreCompact, harness.HookEventPostCompact}, events)
	assert.Equal(t, 5, driver.Calls())
	fixture.stop()
}

func TestWorkflowCompactionHookRefusesInvalidInput(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		event harness.HookEvent
		round int
	}{
		{name: "event", event: harness.HookEventTurnStart},
		{name: "round", event: harness.HookEventPreCompact, round: -1},
		{name: "post payload", event: harness.HookEventPostCompact},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			engine := &workflowExecution{}
			_, err := engine.compactionHook(t.Context(), workflowCompactionHookInput{Event: tc.event, Round: workflowRoundInput{Round: tc.round, Execution: workflowExecutionInput{MaxRounds: 1}}})
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}

func TestNativeWorkflowCompactionHooksRunAroundSavedSummary(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		child bool
	}{
		{name: "root"}, {name: "child", child: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var fixture executionLoopFixture
			var child *models.AgentRun
			var driver *elelemtest.ScriptedDriver
			if tc.child {
				fixture, child, driver = nativeChildCompactionFixture(t)
			} else {
				fixture, _, driver = nativeRootCompactionFixture(t)
			}
			var events []harness.HookEvent
			var payloads []compactionHookPayload
			fixture.base.runtime.hookCallback = func(ctx context.Context, input hooks.Invocation) (hooks.Outcome, error) {
				if input.Event != harness.HookEventPreCompact && input.Event != harness.HookEventPostCompact {
					return hooks.Outcome{}, nil
				}
				page, err := fixture.base.store.ListExecutionActivities(ctx, fixture.input.SessionID, fixture.input.ExecutionID, session.ListEventsOptions{})
				if err != nil {
					return hooks.Outcome{}, ctxerrors.Wrap(err, "read compaction hook checkpoints")
				}
				found := false
				for _, activity := range page.Items {
					if activity.Name == "compactionHook" && activity.State == models.ActivityStateRunning {
						found = activity.ReplayPolicy == models.ActivityReplayPolicyReconcile
					}
				}
				if !found {
					return hooks.Outcome{}, ctxerrors.Wrap(commerr.ErrInvalidState, "compaction hook lacks effect checkpoint")
				}
				var head *models.Compaction
				if child == nil {
					head, err = fixture.base.store.LatestCompaction(ctx, fixture.input.SessionID)
				} else {
					stored, readErr := fixture.base.store.LatestAgentRunCompaction(ctx, child.SessionID, child.ID)
					err = readErr
					if stored != nil {
						head = &models.Compaction{ID: stored.ID}
					}
					require.Equal(t, &child.ID, input.AgentRunID)
				}
				if err != nil {
					return hooks.Outcome{}, ctxerrors.Wrap(err, "read compaction hook summary")
				}
				if input.Event == harness.HookEventPreCompact {
					assert.Nil(t, head)
					assert.Equal(t, 3, driver.Calls())
				} else {
					assert.NotNil(t, head)
					assert.Equal(t, 4, driver.Calls())
				}
				var payload compactionHookPayload
				if err := json.Unmarshal(input.Input, &payload); err != nil {
					return hooks.Outcome{}, ctxerrors.Wrap(err, "decode compaction hook payload")
				}
				assert.Equal(t, fixture.input.SessionID, input.SessionID)
				events = append(events, input.Event)
				payloads = append(payloads, payload)

				return hooks.Outcome{Injections: []string{"ignored compaction injection"}}, nil
			}
			dispatch := newWorkflowDispatch(t.Context(), fixture.base.runtime, fixture.engine.options.GenerationID, nil)
			dispatch.bind(&fixture.engine.options)
			input := workflowRoundInput{Execution: fixture.input}
			result, err := fixture.engine.compactRound(t.Context(), input)
			require.NoError(t, err)
			require.NotNil(t, result.Output.Compaction)
			require.Equal(t, []harness.HookEvent{harness.HookEventPreCompact, harness.HookEventPostCompact}, events)
			require.Len(t, payloads, 2)
			assert.Equal(t, payloads[0].UnitsCovered, payloads[1].UnitsCovered)
			assert.Equal(t, payloads[0].MessagesCovered, payloads[1].MessagesCovered)
			assert.Positive(t, payloads[0].EstimatedTokens)
			assert.Positive(t, payloads[0].UnitsCovered)
			assert.Zero(t, payloads[0].SummaryTokens)
			assert.Equal(t, result.Output.Compaction.SummaryTokenCount, payloads[1].SummaryTokens)
			assert.NotContains(t, string(result.Output.Context), "ignored compaction injection")
			repeated, err := fixture.engine.compactRound(t.Context(), input)
			require.NoError(t, err)
			assert.Equal(t, result.Output, repeated.Output)
			assert.Len(t, events, 2)
			assert.Equal(t, 4, driver.Calls())
			assertPostCompactionContext(t, fixture, result.Output.Context)
		})
	}
}

func compactionHookFixture(t *testing.T, child bool, turns ...elelemtest.Turn) (executionLoopFixture, *elelemtest.ScriptedDriver) {
	t.Helper()
	var fixture executionLoopFixture
	var driver *elelemtest.ScriptedDriver
	if child {
		fixture, _, driver = nativeChildCompactionFixture(t, turns...)
	} else {
		fixture, _, driver = nativeRootCompactionFixture(t, turns...)
	}
	dispatch := newWorkflowDispatch(t.Context(), fixture.base.runtime, fixture.engine.options.GenerationID, nil)
	dispatch.bind(&fixture.engine.options)

	return fixture, driver
}

func TestWorkflowCompactionHooksReuseAcrossModelRetryAndSQLiteReopen(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		child bool
	}{
		{name: "root"}, {name: "child", child: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, driver := compactionHookFixture(t, tc.child, elelemtest.Turn{Err: commerr.ErrFetchFailed}, elelemtest.Text("retry summary"))
			var events []harness.HookEvent
			fixture.base.runtime.hookCallback = func(_ context.Context, input hooks.Invocation) (hooks.Outcome, error) {
				if input.Event == harness.HookEventPreCompact || input.Event == harness.HookEventPostCompact {
					events = append(events, input.Event)
				}

				return hooks.Outcome{}, nil
			}
			input := workflowRoundInput{Execution: fixture.input}
			_, err := fixture.engine.compactRound(t.Context(), input)
			require.ErrorIs(t, err, commerr.ErrFetchFailed)
			assert.Equal(t, []harness.HookEvent{harness.HookEventPreCompact}, events)
			result, err := fixture.engine.compactRound(t.Context(), input)
			require.NoError(t, err)
			assert.Equal(t, []harness.HookEvent{harness.HookEventPreCompact, harness.HookEventPostCompact}, events)
			assert.Equal(t, 5, driver.Calls())
			require.NoError(t, fixture.base.handle.Close())
			handle, err := db.Open(t.Context(), db.Config{Directory: fixture.base.stateDirectory})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, handle.Close()) })
			store, err := session.NewStore(handle, session.Options{})
			require.NoError(t, err)
			fixture.engine.options.Store = store
			fixture.engine.options.GenerationID = uuid.New()
			reused, err := fixture.engine.compactRound(t.Context(), input)
			require.NoError(t, err)
			assert.Equal(t, result.Output, reused.Output)
			assert.Len(t, events, 2)
			assert.Equal(t, 5, driver.Calls())
		})
	}
}

func TestWorkflowCompactionPreDenialPreventsSummary(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		child bool
	}{
		{name: "root"}, {name: "child", child: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, driver := compactionHookFixture(t, tc.child)
			calls := 0
			fixture.base.runtime.hookCallback = func(_ context.Context, input hooks.Invocation) (hooks.Outcome, error) {
				if input.Event == harness.HookEventPreCompact {
					calls++

					return hooks.Outcome{}, &hooks.DenialError{Reason: "fixture compaction denial"}
				}

				return hooks.Outcome{}, nil
			}
			for range 2 {
				_, err := fixture.engine.compactRound(t.Context(), workflowRoundInput{Execution: fixture.input})
				require.ErrorIs(t, err, hooks.ErrDenied)
			}
			assert.Equal(t, 1, calls)
			assert.Equal(t, 3, driver.Calls())
		})
	}
}

func TestWorkflowCompactionPostFailurePreservesSummary(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		child bool
	}{
		{name: "root"}, {name: "child", child: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, driver := compactionHookFixture(t, tc.child)
			calls := 0
			fixture.base.runtime.hookCallback = func(_ context.Context, input hooks.Invocation) (hooks.Outcome, error) {
				if input.Event == harness.HookEventPostCompact {
					calls++

					return hooks.Outcome{}, commerr.ErrFetchFailed
				}

				return hooks.Outcome{}, nil
			}
			for range 2 {
				result, err := fixture.engine.compactRound(t.Context(), workflowRoundInput{Execution: fixture.input})
				require.NoError(t, err)
				require.NotNil(t, result.Output.Compaction)
				assertPostCompactionContext(t, fixture, result.Output.Context)
			}
			assert.Equal(t, 1, calls)
			assert.Equal(t, 4, driver.Calls())
		})
	}
}

func TestWorkflowCompactionInterruptedHooksRequireResolution(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name       string
		child      bool
		event      harness.HookEvent
		modelCalls int
	}{
		{name: "root pre", event: harness.HookEventPreCompact, modelCalls: 3},
		{name: "root post", event: harness.HookEventPostCompact, modelCalls: 4},
		{name: "child pre", child: true, event: harness.HookEventPreCompact, modelCalls: 3},
		{name: "child post", child: true, event: harness.HookEventPostCompact, modelCalls: 4},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, driver := compactionHookFixture(t, tc.child)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			effects := 0
			fixture.base.runtime.hookCallback = func(ctx context.Context, input hooks.Invocation) (hooks.Outcome, error) {
				if input.Event == tc.event {
					effects++
					if effects == 1 {
						cancel()

						return hooks.Outcome{}, ctxerrors.Wrap(ctx.Err(), "interrupt compaction hook fixture")
					}
				}

				return hooks.Outcome{}, nil
			}
			input := workflowRoundInput{Execution: fixture.input}
			result, err := fixture.engine.compactRound(ctx, input)
			require.NoError(t, err)
			assert.Equal(t, session.ActivityDecisionReconcile, result.Decision)
			result, err = fixture.engine.compactRound(t.Context(), input)
			require.NoError(t, err)
			assert.Equal(t, session.ActivityDecisionReconcile, result.Decision)
			assert.Equal(t, 1, effects)
			assert.Equal(t, tc.modelCalls, driver.Calls())
			page, err := fixture.base.store.ListExecutionActivities(t.Context(), fixture.input.SessionID, fixture.input.ExecutionID, session.ListEventsOptions{})
			require.NoError(t, err)
			var uncertain *models.ExecutionActivity
			for _, activity := range page.Items {
				if activity.State == models.ActivityStateUncertain {
					require.Nil(t, uncertain)
					uncertain = activity
				}
			}
			require.NotNil(t, uncertain)
			_, err = fixture.base.store.ResolveExecutionActivity(t.Context(), fixture.input.SessionID, uncertain.ID, session.ResolveActivityInput{
				Attempt: uncertain.Attempt, Resolution: session.ActivityResolutionRetry, Evidence: "fixture inspected the interrupted hook effect and permits retry",
			})
			require.NoError(t, err)
			result, err = fixture.engine.compactRound(t.Context(), input)
			require.NoError(t, err)
			require.NotNil(t, result.Output.Compaction)
			assert.Equal(t, 2, effects)
			assert.Equal(t, 4, driver.Calls())
		})
	}
}

func TestWorkflowCompactionUnderBudgetInvokesNoHooks(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		child bool
	}{
		{name: "root"}, {name: "child", child: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, driver := compactionHookFixture(t, tc.child)
			var conversation workflowConversation
			require.NoError(t, json.Unmarshal(fixture.input.Context, &conversation))
			conversation.MaxContextTokens = 1000
			encoded, err := json.Marshal(conversation)
			require.NoError(t, err)
			fixture.input.Context = encoded
			calls := 0
			fixture.base.runtime.hookCallback = func(_ context.Context, input hooks.Invocation) (hooks.Outcome, error) {
				if input.Event == harness.HookEventPreCompact || input.Event == harness.HookEventPostCompact {
					calls++
				}

				return hooks.Outcome{}, nil
			}
			result, err := fixture.engine.compactRound(t.Context(), workflowRoundInput{Execution: fixture.input})
			require.NoError(t, err)
			assert.Nil(t, result.Output.Compaction)
			assert.Equal(t, 0, calls)
			assert.Equal(t, 3, driver.Calls())
		})
	}
}
