package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowTerminalHookCheckpointReusesCommittedEffect(t *testing.T) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), true)
	var effects atomic.Int64
	fixture.engine.options.TerminalHooks = func(_ context.Context,
		input workflowFinishInput,
	) (workflowFinishInput, error) {
		effects.Add(1)

		return input, nil
	}
	input := callbackFinishInput(fixture)
	first, err := fixture.engine.terminalHooks(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionExecute, first.Decision)
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
	reused, err := fixture.engine.terminalHooks(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	assert.Equal(t, first.Output, reused.Output)
	assert.Equal(t, int64(1), effects.Load())
	page, err := store.ListExecutionActivities(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, terminalHooksActivityName, page.Items[0].Name)
	assert.Equal(t, models.ActivityReplayPolicyReconcile,
		page.Items[0].ReplayPolicy)
}

func TestWorkflowTerminalHookInterruptionRequiresInspection(t *testing.T) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), true)
	var effects atomic.Int64
	fixture.engine.options.TerminalHooks = func(context.Context,
		workflowFinishInput,
	) (workflowFinishInput, error) {
		effects.Add(1)

		return workflowFinishInput{}, commerr.ErrFetchFailed
	}
	input := callbackFinishInput(fixture)
	first, err := fixture.engine.terminalHooks(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReconcile, first.Decision)
	repeated, err := fixture.engine.terminalHooks(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReconcile, repeated.Decision)
	assert.Equal(t, int64(1), effects.Load())
}

func TestWorkflowTerminalHooksRejectInvalidRound(t *testing.T) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), true)
	testCases := []struct {
		name  string
		round int
	}{
		{name: "negative", round: -1},
		{name: "exhausted", round: fixture.input.MaxRounds},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := callbackFinishInput(fixture)
			input.Round = tc.round
			_, err := fixture.engine.terminalHooks(t.Context(), input)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}
