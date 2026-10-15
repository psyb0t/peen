package agent

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rootPhaseProjectionFixture(t *testing.T) (
	executionLoopFixture, *rootWorkflowCallbacks, *rootCallbackScope,
	workflowToolPhaseInput,
) {
	t.Helper()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(runtimeToolCallID, toolNameReadFile,
			runtimeToolArguments(t, runtimeToolFileName)))
	fixture, callbacks, shared := newRootCallbackFixture(t, driver)
	model, err := fixture.engine.modelRound(t.Context(), workflowRoundInput{
		Execution: fixture.input,
	})
	require.NoError(t, err)
	input := workflowToolPhaseInput{
		Tool: workflowToolInput{
			Round: workflowRoundInput{Execution: fixture.input},
			Call:  model.Output.Calls[0],
		}, Phase: workflowToolPhasePre,
	}
	input.Tool.Round.Execution.Context = model.Output.Context

	return fixture, callbacks, shared, input
}

func TestRootPhaseProjectionPauseAfterCommitKeepsSavedResult(t *testing.T) {
	t.Parallel()
	fixture, _, _, input := rootPhaseProjectionFixture(t)
	guard := &phaseProjectionGuardStore{
		Store:            fixture.base.store,
		pauseAfterCommit: true,
	}
	fixture.engine.options.Store = guard
	paused, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionPaused, paused.Decision)
	_, err = fixture.base.store.ResumeExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	reused, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	assert.Equal(t, 1, guard.checked)
}

func TestRootPhaseProjectionRejectsChangedCommittedIdentity(t *testing.T) {
	t.Parallel()
	fixture, callbacks, _, input := rootPhaseProjectionFixture(t)
	result, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	input.Tool.Call.ID = "changedCall"
	require.ErrorIs(t, callbacks.afterToolPhase(t.Context(), input, result.Output),
		commerr.ErrConflict)
}

func TestRootPhaseProjectionRetriesSinkWithoutDuplicatingCommittedMemory(
	t *testing.T,
) {
	t.Parallel()
	fixture, _, shared, input := rootPhaseProjectionFixture(t)
	delivered := 0
	reject := true
	shared.prepared.turn.sink = func(Event) error {
		if delivered == 1 && reject {
			reject = false

			return ctxerrors.Wrap(commerr.ErrConflict, "sink unavailable")
		}
		delivered++

		return nil
	}
	first, err := fixture.engine.toolPhase(t.Context(), input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	require.NotNil(t, first.Output.RootProjection)
	before := len(shared.prepared.turn.events)
	reused, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	assert.Equal(t, before, len(shared.prepared.turn.events))
	assert.Equal(t, len(reused.Output.RootProjection.Transcript.Events), delivered)
	assert.True(t, shared.prepared.turn.pendingTranscript().isEmpty())
}

func TestRootPhaseProjectionReusesCommittedEventsAfterSQLiteReopen(
	t *testing.T,
) {
	t.Parallel()
	fixture, _, _, input := rootPhaseProjectionFixture(t)
	first, err := fixture.engine.toolPhase(t.Context(), input)
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
	fixture.base.runtime.store = store
	delivered := 0
	callbacks := newRootWorkflowCallbacks(fixture.base.runtime,
		fixture.engine.options.GenerationID, func(event Event) error {
			if event.Type == EventTypeToolUse {
				delivered++
			}

			return nil
		})
	callbacks.bind(&fixture.engine.options)
	for range 2 {
		reused, err := fixture.engine.toolPhase(t.Context(), input)
		require.NoError(t, err)
		assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
		assert.Equal(t, first.Output, reused.Output)
	}
	assert.Equal(t, 1, delivered)
	page, err := store.ListEvents(t.Context(), fixture.input.SessionID,
		session.ListEventsOptions{EventTypes: []string{EventTypeToolUse}})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
}
