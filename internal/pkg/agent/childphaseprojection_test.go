package agent

import (
	"context"
	"testing"

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

type childPhaseSinglePageStore struct {
	*session.Store
	pages int
}

func TestChildPhaseProjectionRetainsReversePublication(t *testing.T) {
	t.Parallel()
	delivered := 0
	fixture, dispatch, input, runID := childPhaseProjectionFixture(t,
		func(event Event) error {
			if event.Type == EventTypeAgentRunToolUse {
				delivered++
			}

			return nil
		})
	shared, err := dispatch.child.resolve(t.Context(), input.Tool.Round)
	require.NoError(t, err)
	committed := make([]*models.AgentRunEvent, 0, 2)
	for range 2 {
		event, err := fixture.base.store.AppendAgentRunEvent(t.Context(),
			input.Tool.Round.Execution.SessionID, runID,
			session.AgentRunEventInput{
				ID: uuid.New(), EventType: EventTypeAgentRunToolUse,
				PayloadJSON: `{}`,
			})
		require.NoError(t, err)
		committed = append(committed, event)
	}
	store := &childPhaseSinglePageStore{Store: fixture.base.store}
	fixture.base.runtime.store = store
	for _, index := range []int{1, 0, 1, 0} {
		require.NoError(t, dispatch.child.deliverChildPhaseEvents(t.Context(),
			shared, committed[index].ID, committed[index:index+1]))
	}
	page, err := fixture.base.store.ListAgentRunEvents(t.Context(),
		input.Tool.Round.Execution.SessionID, runID,
		session.ListAgentRunEventsOptions{})
	require.NoError(t, err)
	retained, next, dropped := shared.actor.run.ReadEvents(0, 0)
	wanted := make([]AgentRunEvent, 0, len(page.Items))
	for _, event := range page.Items {
		wanted = append(wanted, retainedChildRegistryEvent(event))
	}
	assert.Equal(t, wanted, retained)
	assert.Equal(t, len(wanted), next)
	assert.Zero(t, dropped)
	assert.Greater(t, store.pages, 1)
	assert.Equal(t, 2, delivered)
}

func (s *childPhaseSinglePageStore) ListAgentRunEvents(
	ctx context.Context,
	sessionID, runID uuid.UUID,
	options session.ListAgentRunEventsOptions,
) (*session.AgentRunEventPage, error) {
	options.Limit = 1
	s.pages++

	return s.Store.ListAgentRunEvents(ctx, sessionID, runID, options)
}

func childPhaseProjectionFixture(t *testing.T, sink EventSink) (
	executionLoopFixture, *workflowDispatch, workflowToolPhaseInput, uuid.UUID,
) {
	t.Helper()
	fixture, input, original := childModelFixture(t, elelemtest.ToolCall(runtimeToolCallID,
		toolNameReadFile, runtimeToolArguments(t, runtimeToolFileName)))
	input.Execution.MaxConcurrentTools = 2
	dispatch := newWorkflowDispatch(t.Context(), fixture.base.runtime,
		fixture.engine.options.GenerationID, sink)
	dispatch.bind(&fixture.engine.options)
	model, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	input.Execution.Context = model.Output.Context

	return fixture, dispatch, workflowToolPhaseInput{
		Tool:  workflowToolInput{Round: input, Call: model.Output.Calls[0]},
		Phase: workflowToolPhasePre,
	}, original.run.ID
}

func TestChildPhaseProjectionReadsEveryPageAndRejectsMissingRecords(t *testing.T) {
	t.Parallel()
	fixture, dispatch, input, runID := childPhaseProjectionFixture(t, nil)
	shared, err := dispatch.child.resolve(t.Context(), input.Tool.Round)
	require.NoError(t, err)
	projection := &childPhaseBuffer{}
	for range 2 {
		event := session.AgentRunEventInput{
			ID:        uuid.New(),
			EventType: EventTypeAgentRunToolUse, PayloadJSON: `{}`,
		}
		_, err := fixture.base.store.AppendAgentRunEvent(t.Context(),
			input.Tool.Round.Execution.SessionID, runID, event)
		require.NoError(t, err)
		projection.Transcript.Events = append(projection.Transcript.Events, event)
	}
	store := &childPhaseSinglePageStore{Store: fixture.base.store}
	fixture.base.runtime.store = store
	events, err := dispatch.child.readChildPhaseEvents(t.Context(),
		shared.actor.run, projection)
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Greater(t, store.pages, 2)
	assert.Equal(t, projection.Transcript.Events[0].ID, events[0].ID)
	projection.Transcript.Events[0].PayloadJSON = `{"changed":true}`
	_, err = dispatch.child.readChildPhaseEvents(t.Context(), shared.actor.run,
		projection)
	require.ErrorIs(t, err, commerr.ErrConflict)
	projection.Transcript.Events[0].PayloadJSON = `{}`
	projection.Transcript.Events = append(projection.Transcript.Events,
		session.AgentRunEventInput{ID: uuid.New()})
	_, err = dispatch.child.readChildPhaseEvents(t.Context(), shared.actor.run,
		projection)
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

func TestChildPhaseProjectionReusesLostAcknowledgement(t *testing.T) {
	t.Parallel()
	delivered := 0
	fixture, _, input, runID := childPhaseProjectionFixture(t,
		func(event Event) error {
			if event.Type == EventTypeAgentRunToolUse {
				delivered++
			}

			return nil
		})
	guard := &childPhaseGuardStore{
		Store: fixture.base.store,
		runID: runID, loseAcknowledgement: true,
	}
	fixture.engine.options.Store = guard
	first, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReconcile, first.Decision)
	assert.Zero(t, delivered)
	for range 2 {
		reused, err := fixture.engine.toolPhase(t.Context(), input)
		require.NoError(t, err)
		assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	}
	assert.Equal(t, 1, delivered)
	assert.Equal(t, 1, guard.checked)
}

func TestChildPhaseProjectionRetriesSinkWithoutAnotherWrite(t *testing.T) {
	t.Parallel()
	blocked := true
	delivered := 0
	fixture, dispatch, input, runID := childPhaseProjectionFixture(t,
		func(event Event) error {
			if event.Type != EventTypeAgentRunToolUse {
				return nil
			}
			if blocked {
				blocked = false

				return ctxerrors.Wrap(commerr.ErrConflict, "sink unavailable")
			}
			delivered++

			return nil
		})
	_, err := fixture.engine.toolPhase(t.Context(), input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	before, err := fixture.base.store.ListAgentRunEvents(t.Context(),
		input.Tool.Round.Execution.SessionID, runID,
		session.ListAgentRunEventsOptions{})
	require.NoError(t, err)
	reused, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	assert.Equal(t, 1, delivered)
	after, err := fixture.base.store.ListAgentRunEvents(t.Context(),
		input.Tool.Round.Execution.SessionID, runID,
		session.ListAgentRunEventsOptions{})
	require.NoError(t, err)
	assert.Equal(t, before, after)
	input.Tool.Call.ID = "changed"
	require.ErrorIs(t, dispatch.child.afterToolPhase(t.Context(), input,
		reused.Output), commerr.ErrConflict)
}

func TestChildPhaseProjectionReusesAfterSQLiteReopen(t *testing.T) {
	t.Parallel()
	fixture, _, input, runID := childPhaseProjectionFixture(t, nil)
	first, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	before, err := fixture.base.store.ListAgentRunEvents(t.Context(),
		input.Tool.Round.Execution.SessionID, runID,
		session.ListAgentRunEventsOptions{})
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
	original := fixture.base.runtime
	replacement, err := NewRuntime(t.Context(), RuntimeOptions{
		Store: store, Resolver: original.resolver, Models: original.models,
		RootAgent: original.rootAgent, DefaultModel: original.defaultModel,
		StartupSessionID:    input.Tool.Round.Execution.SessionID,
		MaxContextTokens:    original.maxContextTokens,
		ModelRequestTimeout: original.modelRequestTimeout,
		WorkerGenerationID:  fixture.engine.options.GenerationID,
		ConfigDirectory:     fixture.base.configDirectory,
	})
	require.NoError(t, err)
	delivered := 0
	dispatch := newWorkflowDispatch(t.Context(), replacement,
		fixture.engine.options.GenerationID, func(event Event) error {
			if event.Type == EventTypeAgentRunToolUse {
				delivered++
			}

			return nil
		})
	dispatch.bind(&fixture.engine.options)
	for range 2 {
		reused, err := fixture.engine.toolPhase(t.Context(), input)
		require.NoError(t, err)
		assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
		assert.Equal(t, first.Output, reused.Output)
	}
	assert.Equal(t, 1, delivered)
	after, err := store.ListAgentRunEvents(t.Context(),
		input.Tool.Round.Execution.SessionID, runID,
		session.ListAgentRunEventsOptions{})
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestChildPhaseProjectionPauseAfterCommitRetainsResult(t *testing.T) {
	t.Parallel()
	delivered := 0
	fixture, _, input, runID := childPhaseProjectionFixture(t,
		func(event Event) error {
			if event.Type == EventTypeAgentRunToolUse {
				delivered++
			}

			return nil
		})
	guard := &childPhaseGuardStore{
		Store: fixture.base.store, runID: runID, pauseAfterCommit: true,
	}
	fixture.engine.options.Store = guard
	paused, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionPaused, paused.Decision)
	assert.Zero(t, delivered)
	_, err = fixture.base.store.ResumeExecution(t.Context(),
		input.Tool.Round.Execution.SessionID, input.Tool.Round.Execution.ExecutionID)
	require.NoError(t, err)
	reused, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	assert.Equal(t, paused.Output, reused.Output)
	assert.Equal(t, 1, guard.checked)
	assert.Equal(t, 1, delivered)
}
