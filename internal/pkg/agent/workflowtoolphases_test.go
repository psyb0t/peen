package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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

const (
	// largePhaseContextBytes is above the default 1 MiB message limit.
	largePhaseContextBytes = 3 << 19
	// singlePhaseContextSlackBytes is the room a phase payload has beside
	// one context copy.
	singlePhaseContextSlackBytes = 64 << 10
)

func toolPhaseTestInput(fixture executionLoopFixture) workflowToolPhaseInput {
	return workflowToolPhaseInput{
		Tool: workflowToolInput{
			Round: workflowRoundInput{Execution: fixture.input},
			Call: elelem.ToolCall{
				ID: runtimeToolCallID, Name: toolNameReadFile,
				Arguments: json.RawMessage(workflowEmptyArguments),
			},
		},
		Phase: workflowToolPhasePre,
	}
}

func TestWorkflowToolPhasesRetainEachCheckpointAfterReopen(t *testing.T) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), true)
	effects := 0
	fixture.engine.options.ToolPhase = func(_ context.Context,
		input workflowToolPhaseInput,
	) (workflowToolPhaseState, error) {
		effects++
		state := input.State
		state.Outcome.Result.Content = string(input.Phase)

		return state, nil
	}
	phases := []workflowToolPhase{
		workflowToolPhasePre, workflowToolPhaseHandler, workflowToolPhaseOutcome,
		workflowToolPhasePost, workflowToolPhaseProject,
	}
	inputs := make([]workflowToolPhaseInput, 0, len(phases))
	outputs := make([]workflowToolPhaseState, 0, len(phases))
	input := toolPhaseTestInput(fixture)
	for _, phase := range phases {
		input.Phase = phase
		inputs = append(inputs, input)
		result, err := fixture.engine.toolPhase(t.Context(), input)
		require.NoError(t, err)
		require.Equal(t, session.ActivityDecisionExecute, result.Decision)
		assert.Equal(t, phase, result.Output.LastPhase)
		outputs = append(outputs, result.Output)
		input.State = result.Output
	}
	fixture.stop()
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
	for index, saved := range inputs {
		reused, err := fixture.engine.toolPhase(t.Context(), saved)
		require.NoError(t, err)
		assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
		assert.Equal(t, outputs[index], reused.Output)
	}
	assert.Equal(t, len(inputs), effects)
	page, err := store.ListExecutionActivities(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, len(inputs))
	for _, activity := range page.Items {
		assert.Equal(t, models.ActivityStateCompleted, activity.State)
		assert.Equal(t, toolPhaseActivityName, activity.Name)
		assert.Equal(t, models.ActivityReplayPolicyReconcile, activity.ReplayPolicy)
	}
}

func TestWorkflowToolPhasesKeepOneContextCopyPastTheMessageLimit(
	t *testing.T,
) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), true)
	// A phase re-encodes the context it was given, as the directory phases
	// do, so its output names a context equal to its input.
	fixture.engine.options.ToolPhase = func(_ context.Context,
		input workflowToolPhaseInput,
	) (workflowToolPhaseState, error) {
		var conversation workflowConversation
		if err := json.Unmarshal(input.Tool.Round.Execution.Context,
			&conversation); err != nil {
			return input.State, ctxerrors.Wrap(err, "decode phase context")
		}
		encoded, err := json.Marshal(conversation)
		if err != nil {
			return input.State, ctxerrors.Wrap(err, "encode phase context")
		}
		state := input.State
		state.Outcome.Context = encoded

		return state, nil
	}
	large, err := json.Marshal(workflowConversation{Messages: []elelem.Message{{
		Role:    elelem.RoleUser,
		Content: elelem.Text(strings.Repeat("x", largePhaseContextBytes)),
	}}})
	require.NoError(t, err)
	input := toolPhaseTestInput(fixture)
	input.Tool.Round.Execution.Context = large
	phases := []workflowToolPhase{
		workflowToolPhasePre, workflowToolPhaseHandler, workflowToolPhaseOutcome,
		workflowToolPhasePost, workflowToolPhaseProject,
	}
	for _, phase := range phases {
		input.Phase = phase
		result, err := fixture.engine.toolPhase(t.Context(), input)
		require.NoError(t, err)
		assert.Empty(t, result.Output.Outcome.Context)
		input.State = result.Output
		advanceWorkflowToolPhase(&input.Tool, &input.State)
	}
	page, err := fixture.base.store.ListExecutionActivities(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, len(phases))
	for _, activity := range page.Items {
		assert.Equal(t, models.ActivityStateCompleted, activity.State)
		assert.Less(t, len(activity.InputJSON),
			len(large)+singlePhaseContextSlackBytes)
		require.NotNil(t, activity.OutputJSON)
		assert.Less(t, len(*activity.OutputJSON), singlePhaseContextSlackBytes)
		assert.JSONEq(t, string(large), activity.ContextJSON)
	}
}

func TestWorkflowToolPhaseRejectsChangedSavedInput(t *testing.T) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), true)
	effects := 0
	fixture.engine.options.ToolPhase = func(_ context.Context,
		input workflowToolPhaseInput,
	) (workflowToolPhaseState, error) {
		effects++

		return input.State, nil
	}
	input := toolPhaseTestInput(fixture)
	_, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	input.Tool.Call.Arguments = json.RawMessage(`{"path":"changed"}`)
	_, err = fixture.engine.toolPhase(t.Context(), input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	assert.Equal(t, 1, effects)
}

func TestWorkflowToolPhaseInterruptionRetainsDecision(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		pause     bool
		decisions []session.ActivityDecision
		state     models.ExecutionState
	}{
		{
			name: "failed phase reaches the model as an error",
			decisions: []session.ActivityDecision{
				session.ActivityDecisionReconcile, session.ActivityDecisionReuse,
			},
			state: models.ExecutionStateRunning,
		},
		{
			name: "explicit pause", pause: true,
			decisions: []session.ActivityDecision{
				session.ActivityDecisionPaused, session.ActivityDecisionPaused,
			},
			state: models.ExecutionStatePaused,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionLoopFixture(t,
				elelemtest.NewScriptedDriver(), true)
			effects := 0
			fixture.engine.options.ToolPhase = func(ctx context.Context,
				_ workflowToolPhaseInput,
			) (workflowToolPhaseState, error) {
				effects++
				if tc.pause {
					require.NoError(t, fixture.engine.pause(ctx,
						fixture.input.SessionID, fixture.input.ExecutionID, "retain work"))
				}

				return workflowToolPhaseState{}, context.Canceled
			}
			input := toolPhaseTestInput(fixture)
			var result workflowActivityResult[workflowToolPhaseState]
			for _, decision := range tc.decisions {
				var err error
				result, err = fixture.engine.toolPhase(t.Context(), input)
				require.NoError(t, err)
				assert.Equal(t, decision, result.Decision)
			}
			assert.Equal(t, 1, effects)
			if !tc.pause {
				assert.True(t, result.Output.Finished)
				assert.True(t, result.Output.Outcome.Result.IsError)
				assert.Equal(t, failedToolResult,
					result.Output.Outcome.Result.Content)
			}
			execution, err := fixture.base.store.GetExecution(t.Context(),
				fixture.input.SessionID, fixture.input.ExecutionID)
			require.NoError(t, err)
			assert.Equal(t, tc.state, execution.State)
		})
	}
}

func TestWorkflowToolPhaseRefusesInvalidIdentityBeforeClaim(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		change func(*workflowToolPhaseInput)
	}{
		{name: "unknown phase", change: func(input *workflowToolPhaseInput) {
			input.Phase = "unknown"
		}},
		{name: "missing predecessor", change: func(input *workflowToolPhaseInput) {
			input.Phase = workflowToolPhaseHandler
		}},
		{name: "reversed predecessor", change: func(input *workflowToolPhaseInput) {
			input.State.LastPhase = workflowToolPhasePost
		}},
		{name: "empty call", change: func(input *workflowToolPhaseInput) {
			input.Tool.Call.ID = " "
		}},
		{name: "empty tool", change: func(input *workflowToolPhaseInput) {
			input.Tool.Call.Name = " "
		}},
		{name: "negative index", change: func(input *workflowToolPhaseInput) {
			input.Tool.Index = -1
		}},
		{name: "negative round", change: func(input *workflowToolPhaseInput) {
			input.Tool.Round.Round = -1
		}},
		{name: "exhausted round", change: func(input *workflowToolPhaseInput) {
			input.Tool.Round.Round = input.Tool.Round.Execution.MaxRounds
		}},
		{name: "missing session", change: func(input *workflowToolPhaseInput) {
			input.Tool.Round.Execution.SessionID = uuid.Nil
		}},
		{name: "finished pre-hook", change: func(input *workflowToolPhaseInput) {
			input.State.Finished = true
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionLoopFixture(t,
				elelemtest.NewScriptedDriver(), true)
			input := toolPhaseTestInput(fixture)
			tc.change(&input)
			_, err := fixture.engine.toolPhase(t.Context(), input)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
			page, err := fixture.base.store.ListExecutionActivities(t.Context(),
				fixture.input.SessionID, fixture.input.ExecutionID,
				session.ListEventsOptions{})
			require.NoError(t, err)
			assert.Empty(t, page.Items)
		})
	}
}

func TestWorkflowToolPhaseRequiresItsCallback(t *testing.T) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), true)
	fixture.engine.options.ToolPhase = nil
	_, err := fixture.engine.toolPhase(t.Context(), toolPhaseTestInput(fixture))
	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
}
