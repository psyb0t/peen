package agent

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type modelDeliveryReadFailure struct {
	*session.Store
}

func TestModelDeliveryWaitsForRootAdmissionWithoutAnotherRequest(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("retained root response"))
	fixture := newExecutionLoopFixture(t, driver, false)
	busy := true
	fixture.engine.options.AfterModelRound = func(context.Context, workflowRoundInput, workflowRoundResult) error {
		if busy {
			return ctxerrors.Wrap(errRootAdmissionBusy, "restore committed root model")
		}

		return nil
	}
	input := workflowRoundInput{Execution: fixture.input}
	first, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionPaused, first.Decision)
	busy = false
	resumed, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, resumed.Decision)
	assert.Equal(t, first.Output, resumed.Output)
	assert.Len(t, driver.Requests(), 1)
}

func TestModelDeliveryCapacityPauseReusesCommittedRequest(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("saved response"))
	fixture := newExecutionLoopFixture(t, driver, false)
	available := false
	fixture.engine.options.AfterModelRound = func(context.Context,
		workflowRoundInput, workflowRoundResult,
	) error {
		if !available {
			return ErrTooManyAgentRuns
		}

		return nil
	}
	input := workflowRoundInput{Execution: fixture.input}
	first, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionPaused, first.Decision)
	assert.Equal(t, 1, driver.Calls())
	available = true
	_, err = fixture.base.store.ResumeExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	resumed, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, resumed.Decision)
	assert.Equal(t, first.Output, resumed.Output)
	assert.Equal(t, 1, driver.Calls())
}

func (s *modelDeliveryReadFailure) GetExecution(
	context.Context,
	uuid.UUID,
	uuid.UUID,
) (*models.Execution, error) {
	return nil, commerr.ErrConflict
}

func TestModelDeliverySkipsInactiveDecisions(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		decision session.ActivityDecision
	}{
		{name: "paused", decision: session.ActivityDecisionPaused},
		{name: "reconcile", decision: session.ActivityDecisionReconcile},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			called := false
			engine := &workflowExecution{options: workflowExecutionOptions{
				AfterModelRound: func(context.Context, workflowRoundInput,
					workflowRoundResult,
				) error {
					called = true

					return nil
				},
			}}
			result := workflowActivityResult[workflowRoundResult]{
				Decision: tc.decision,
			}
			got, err := engine.deliverModelRound(t.Context(),
				workflowRoundInput{}, result)
			require.NoError(t, err)
			assert.Equal(t, result, got)
			assert.False(t, called)
		})
	}
}

func TestModelDeliveryPreservesCallbackAndReadErrors(t *testing.T) {
	t.Parallel()
	engine := &workflowExecution{options: workflowExecutionOptions{
		Store:     &modelDeliveryReadFailure{},
		Lifecycle: t.Context(),
		AfterModelRound: func(context.Context, workflowRoundInput,
			workflowRoundResult,
		) error {
			return context.Canceled
		},
	}}
	_, err := engine.deliverModelRound(t.Context(), workflowRoundInput{},
		workflowActivityResult[workflowRoundResult]{
			Decision: session.ActivityDecisionReuse,
		})
	require.ErrorIs(t, err, commerr.ErrConflict)
	require.ErrorIs(t, err, context.Canceled)
}

func TestModelDeliveryRecoversCallbackPanic(t *testing.T) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), false)
	fixture.engine.options.AfterModelRound = func(context.Context,
		workflowRoundInput, workflowRoundResult,
	) error {
		panic("delivery panic")
	}
	_, err := fixture.engine.deliverModelRound(t.Context(), workflowRoundInput{
		Execution: fixture.input,
	}, workflowActivityResult[workflowRoundResult]{
		Decision: session.ActivityDecisionReuse,
	})
	require.Error(t, err)
}
