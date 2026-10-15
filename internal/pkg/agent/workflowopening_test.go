package agent

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpeningHookCheckpointReusesCompletedEffects(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		event harness.HookEvent
	}{
		{name: "preUserMessage", event: harness.HookEventPreUserMessage},
		{name: "sessionStart", event: harness.HookEventSessionStart},
		{name: "postUserMessage", event: harness.HookEventPostUserMessage},
		{name: "turnStart", event: harness.HookEventTurnStart},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), true)
			calls := 0
			fixture.engine.options.OpeningHook = func(_ context.Context, input workflowOpeningInput) (workflowOpeningResult, error) {
				calls++

				return workflowOpeningResult{Context: input.Execution.Context}, nil
			}
			input := workflowOpeningInput{Execution: fixture.input, Event: tc.event}
			first, err := fixture.engine.openingHook(t.Context(), input)
			require.NoError(t, err)
			assert.Equal(t, session.ActivityDecisionExecute, first.Decision)
			fixture.engine.options.GenerationID = uuid.New()
			second, err := fixture.engine.openingHook(t.Context(), input)
			require.NoError(t, err)
			assert.Equal(t, session.ActivityDecisionReuse, second.Decision)
			assert.Equal(t, first.Output, second.Output)
			assert.Equal(t, 1, calls)
		})
	}
}

func TestOpeningHookInterruptionRequiresInspectedResolution(t *testing.T) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), true)
	calls := 0
	fixture.engine.options.OpeningHook = func(_ context.Context, input workflowOpeningInput) (workflowOpeningResult, error) {
		calls++
		if calls == 1 {
			return workflowOpeningResult{}, ctxerrors.Wrap(context.Canceled, "interrupt opening effect")
		}

		return workflowOpeningResult{Context: input.Execution.Context}, nil
	}
	input := workflowOpeningInput{Execution: fixture.input, Event: harness.HookEventTurnStart}
	for range 2 {
		result, err := fixture.engine.openingHook(t.Context(), input)
		require.NoError(t, err)
		assert.Equal(t, session.ActivityDecisionReconcile, result.Decision)
	}
	assert.Equal(t, 1, calls)
	page, err := fixture.base.store.ListExecutionActivities(t.Context(), fixture.input.SessionID, fixture.input.ExecutionID, session.ListEventsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	_, err = fixture.base.store.ResolveExecutionActivity(t.Context(), fixture.input.SessionID, page.Items[0].ID, session.ResolveActivityInput{
		Attempt: page.Items[0].Attempt, Resolution: session.ActivityResolutionRetry, Evidence: "fixture inspected the effect and permits retry",
	})
	require.NoError(t, err)
	result, err := fixture.engine.openingHook(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionExecute, result.Decision)
	assert.Equal(t, 2, calls)
}

func TestOpeningHookRefusesOtherEvents(t *testing.T) {
	t.Parallel()
	engine := &workflowExecution{}
	_, err := engine.openingHook(t.Context(), workflowOpeningInput{Event: harness.HookEventTurnStop})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

func TestOpeningHookDeliveryPreservesConcurrentPause(t *testing.T) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), true)
	fixture.engine.options.AfterOpeningHook = func(ctx context.Context, input workflowOpeningInput, _ workflowOpeningResult) error {
		if err := fixture.engine.pause(ctx, input.Execution.SessionID, input.Execution.ExecutionID, "pause during saved opening delivery"); err != nil {
			return ctxerrors.Wrap(err, "pause opening delivery fixture")
		}

		return ctxerrors.Wrap(context.Canceled, "interrupt opening delivery fixture")
	}
	result, err := fixture.engine.openingHook(t.Context(), workflowOpeningInput{
		Execution: fixture.input, Event: harness.HookEventTurnStart,
	})
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionPaused, result.Decision)
}
