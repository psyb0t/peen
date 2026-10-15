package agent

import (
	"context"
	"testing"

	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolDeliveryWaitsForRootAdmissionWithoutAnotherEffect(t *testing.T) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), false)
	busy, effects := true, 0
	fixture.engine.options.ToolPhase = func(context.Context, workflowToolPhaseInput) (workflowToolPhaseState, error) {
		effects++

		return workflowToolPhaseState{}, nil
	}
	fixture.engine.options.AfterToolPhase = func(context.Context, workflowToolPhaseInput, workflowToolPhaseState) error {
		if busy {
			return errRootAdmissionBusy
		}

		return nil
	}
	input := toolPhaseTestInput(fixture)
	first, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionPaused, first.Decision)
	busy = false
	resumed, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, resumed.Decision)
	assert.Equal(t, first.Output, resumed.Output)
	assert.Equal(t, 1, effects)
}

func TestToolDeliveryCapacityPauseReusesCommittedPhase(t *testing.T) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), false)
	available, effects := false, 0
	fixture.engine.options.ToolPhase = func(context.Context,
		workflowToolPhaseInput,
	) (workflowToolPhaseState, error) {
		effects++

		return workflowToolPhaseState{}, nil
	}
	fixture.engine.options.AfterToolPhase = func(context.Context,
		workflowToolPhaseInput, workflowToolPhaseState,
	) error {
		if !available {
			return ErrTooManyAgentRuns
		}

		return nil
	}
	input := toolPhaseTestInput(fixture)
	first, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionPaused, first.Decision)
	available = true
	_, err = fixture.base.store.ResumeExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	resumed, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, resumed.Decision)
	assert.Equal(t, first.Output, resumed.Output)
	assert.Equal(t, 1, effects)
}
