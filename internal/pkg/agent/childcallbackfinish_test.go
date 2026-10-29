package agent

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/go-workflows/client"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// lateChildEventCount spans more than one restore read page, so the
	// backward search crosses a page boundary.
	lateChildEventCount = session.MaximumPageLimit + 3
	// lateChildEventFeedBase keeps planted feed sequences clear of the ones
	// the store allocated.
	lateChildEventFeedBase = 1_000_000
)

func TestChildCallbackCancellationBeforeBindingRetiresRegistry(t *testing.T) {
	t.Parallel()
	fixture, input, sink := childModelFixture(t)
	stored, err := fixture.base.store.GetAgentRun(t.Context(),
		input.Execution.SessionID, sink.run.ID)
	require.NoError(t, err)
	registry, err := fixture.base.runtime.sessionAgentRuns(stored.SessionID)
	require.NoError(t, err)
	run, err := registry.restoreWorkflowRun(stored, func() {}, nil)
	require.NoError(t, err)
	_, _, err = fixture.base.store.RequestAgentRunCancellation(t.Context(),
		stored.SessionID, stored.ID)
	require.NoError(t, err)
	execution, err := fixture.base.store.GetExecution(t.Context(),
		stored.SessionID, input.Execution.ExecutionID)
	require.NoError(t, err)
	callbacks := newChildWorkflowCallbacks(t.Context(), fixture.base.runtime,
		nil)
	require.NoError(t, callbacks.retireChildCallback(t.Context(),
		callbacks.slot(execution.ID), execution))
	assert.Equal(t, AgentRunStateCancelled, run.Snapshot().State)
	select {
	case <-run.Done():
	default:
		t.Fatal("cancelled unbound child completion remains open")
	}
}

// A session stored before the store refused late events can hold deltas a
// stream wrote after its child was stopped. Restore still finds the
// cancellation instead of refusing to start the session's worker.
func TestChildCallbackCancellationSkipsEventsStoredAfterTheTerminalEvent(
	t *testing.T,
) {
	t.Parallel()
	fixture, input, sink := childModelFixture(t)
	sessionID := input.Execution.SessionID
	_, _, err := fixture.base.store.RequestAgentRunCancellation(t.Context(),
		sessionID, sink.run.ID)
	require.NoError(t, err)
	stored, err := fixture.base.store.GetAgentRun(t.Context(), sessionID,
		sink.run.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.EndedAt)
	require.NoError(t, sink.emit(t.Context(), EventTypeAgentRunThinkingDelta,
		map[string]string{"text": "late"}))
	unchanged, err := fixture.base.store.GetAgentRun(t.Context(), sessionID,
		sink.run.ID)
	require.NoError(t, err)
	assert.Equal(t, stored.EventCount, unchanged.EventCount)
	plantLateChildEvents(t, fixture.base.handle, stored)
	execution, err := fixture.base.store.GetExecution(t.Context(), sessionID,
		input.Execution.ExecutionID)
	require.NoError(t, err)
	callbacks := newChildWorkflowCallbacks(t.Context(), fixture.base.runtime,
		nil)
	require.NoError(t, callbacks.retireChildCallback(t.Context(),
		callbacks.slot(execution.ID), execution))
	registry, err := fixture.base.runtime.sessionAgentRuns(sessionID)
	require.NoError(t, err)
	run, found := registry.Get(stored.ID)
	require.True(t, found)
	assert.Equal(t, AgentRunStateCancelled, run.Snapshot().State)
}

// plantLateChildEvents writes thinking deltas after a run's terminal event the
// way an older store did, bypassing the refusal the store has now.
func plantLateChildEvents(
	t *testing.T,
	handle *db.Handle,
	run *models.AgentRun,
) {
	t.Helper()
	query := repositories.Use(handle.GormDB)
	for index := range lateChildEventCount {
		sequence := run.EventCount + int64(index) + 1
		require.NoError(t, query.AgentRunEvent.WithContext(t.Context()).Create(
			&models.AgentRunEvent{
				ID: uuid.New(), SessionID: run.SessionID, AgentRunID: run.ID,
				Sequence: sequence, FeedSequence: lateChildEventFeedBase + sequence,
				EventType:   EventTypeAgentRunThinkingDelta,
				PayloadJSON: `{"text":"late"}`, CreatedAt: time.Now().UTC(),
			}))
	}
	_, err := query.AgentRun.WithContext(t.Context()).
		Where(query.AgentRun.ID.Eq(run.ID)).
		UpdateSimple(query.AgentRun.EventCount.Add(lateChildEventCount))
	require.NoError(t, err)
}

func TestChildCallbackFinishRetiresRegistryAndPublishesCommittedEvent(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childModelFixture(t, elelemtest.Text("finished child"))
	var terminal []Event
	callbacks := newChildWorkflowCallbacks(t.Context(), fixture.base.runtime,
		func(event Event) error {
			if event.Type == EventTypeAgentRunCompleted {
				terminal = append(terminal, event)
			}

			return nil
		})
	callbacks.bind(&fixture.engine.options)
	shared, err := callbacks.resolve(t.Context(), input)
	require.NoError(t, err)
	fixture.input = input.Execution
	fixture.input.MaxConcurrentTools = 1
	instance := startCompactingWorkflow(t, fixture)
	_, err = client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	snapshot := shared.actor.run.Snapshot()
	assert.Equal(t, AgentRunStateCompleted, snapshot.State)
	select {
	case <-shared.actor.run.Done():
	default:
		t.Fatal("committed child did not close Done")
	}
	require.Len(t, terminal, 1)
	stored, err := fixture.base.store.GetAgentRun(t.Context(),
		input.Execution.SessionID, shared.actor.run.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.EndedAt)
	assert.Equal(t, *stored.EndedAt, snapshot.EndedAt)
	assert.Empty(t, shared.actor.run.inbox.close())
	events, err := fixture.base.store.ListAgentRunEvents(t.Context(),
		input.Execution.SessionID, shared.actor.run.ID,
		session.ListAgentRunEventsOptions{Cursor: stored.EventCount - 1, Limit: 1})
	require.NoError(t, err)
	require.Len(t, events.Items, 1)
	assert.Equal(t, models.AgentRunStateCompleted, stored.State)
	assert.Equal(t, EventTypeAgentRunCompleted, events.Items[0].EventType)
	fixture.stop()
}

func TestChildCallbackFinishRetriesLiveFailureWithoutAnotherEvent(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childModelFixture(t, elelemtest.Text("retained final"))
	attempts := 0
	callbacks := newChildWorkflowCallbacks(t.Context(), fixture.base.runtime,
		func(event Event) error {
			if event.Type != EventTypeAgentRunCompleted {
				return nil
			}
			attempts++
			if attempts == 1 {
				return commerr.ErrFetchFailed
			}

			return nil
		})
	callbacks.bind(&fixture.engine.options)
	shared, err := callbacks.resolve(t.Context(), input)
	require.NoError(t, err)
	fixture.input = input.Execution
	fixture.input.MaxConcurrentTools = 1
	instance := startCompactingWorkflow(t, fixture)
	_, err = client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, 2, attempts)
	finish := workflowFinishResult{Finish: session.FinishExecutionInput{
		State: models.ExecutionStateCompleted,
		Child: &session.ExecutionFinishChild{State: models.AgentRunStateCompleted},
	}}
	finalInput := workflowFinishInput{Execution: fixture.input}
	require.NoError(t, callbacks.afterFinish(t.Context(), finalInput, finish))
	assert.Equal(t, 2, attempts)
	stored, err := fixture.base.store.GetAgentRun(t.Context(),
		input.Execution.SessionID, shared.actor.run.ID)
	require.NoError(t, err)
	page, err := fixture.base.store.ListAgentRunEvents(t.Context(),
		input.Execution.SessionID, stored.ID,
		session.ListAgentRunEventsOptions{Cursor: stored.EventCount - 1, Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	cold := newChildWorkflowCallbacks(t.Context(), fixture.base.runtime, nil)
	require.NoError(t, cold.afterFinish(t.Context(), finalInput, finish))
	after, err := fixture.base.store.GetAgentRun(t.Context(),
		input.Execution.SessionID, stored.ID)
	require.NoError(t, err)
	assert.Equal(t, stored, after)
	finish.Finish.Child.State = models.AgentRunStateFailed
	require.ErrorIs(t, cold.afterFinish(t.Context(), finalInput, finish),
		commerr.ErrConflict)
	fixture.stop()
}

func TestChildCallbackFinishReopensSQLiteWithoutRestoringAnActiveScope(
	t *testing.T,
) {
	t.Parallel()
	fixture, input, original := childModelFixture(t,
		elelemtest.Text("stored terminal child"))
	callbacks := newChildWorkflowCallbacks(t.Context(), fixture.base.runtime, nil)
	callbacks.bind(&fixture.engine.options)
	fixture.input = input.Execution
	fixture.input.MaxConcurrentTools = 1
	instance := startCompactingWorkflow(t, fixture)
	_, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	fixture.stop()
	stored, err := fixture.base.store.GetAgentRun(t.Context(),
		input.Execution.SessionID, original.run.ID)
	require.NoError(t, err)
	require.NoError(t, fixture.base.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{
		Directory: fixture.base.stateDirectory,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)
	runtime, err := NewRuntime(t.Context(), RuntimeOptions{
		Store: store, Resolver: fixture.base.runtime.resolver,
		Models: fixture.base.runtime.models, RootAgent: runtimeTestAgentName,
		DefaultModel:        runtimeTestModelReference,
		DefaultWorkspace:    fixture.base.workspace,
		ConfigDirectory:     fixture.base.configDirectory,
		ModelRequestTimeout: runtimeTestTurnTimeout,
		MaxContextTokens:    runtimeTestMaxContextTokens,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, runtime.Shutdown(context.WithoutCancel(t.Context())))
	})
	cold := newChildWorkflowCallbacks(t.Context(), runtime, nil)
	result := workflowFinishResult{Finish: session.FinishExecutionInput{
		State: models.ExecutionStateCompleted,
		Child: &session.ExecutionFinishChild{State: models.AgentRunStateCompleted},
	}}
	final := workflowFinishInput{Execution: input.Execution}
	require.NoError(t, cold.afterFinish(t.Context(), final, result))
	require.NoError(t, cold.afterFinish(t.Context(), final, result))
	after, err := store.GetAgentRun(t.Context(), input.Execution.SessionID,
		original.run.ID)
	require.NoError(t, err)
	assert.Equal(t, stored, after)
	registry, err := runtime.sessionAgentRuns(input.Execution.SessionID)
	require.NoError(t, err)
	run, found := registry.Get(original.run.ID)
	require.True(t, found)
	assert.Equal(t, AgentRunStateCompleted, run.Snapshot().State)
	select {
	case <-run.Done():
	default:
		t.Fatal("restored terminal child has an open completion channel")
	}
}

func TestChildCallbackFinishProjectsFailedOutcomeAndReusesIt(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childModelFixture(t)
	delivered := 0
	callbacks := newChildWorkflowCallbacks(t.Context(), fixture.base.runtime,
		func(event Event) error {
			if event.Type == EventTypeAgentRunFailed {
				delivered++
			}

			return nil
		})
	callbacks.bind(&fixture.engine.options)
	shared, err := callbacks.resolve(t.Context(), input)
	require.NoError(t, err)
	final := workflowFinishInput{
		Execution: input.Execution,
		Result: workflowFinishResult{
			Result: workflowRoundResult{Context: input.Execution.Context},
			Finish: session.FinishExecutionInput{
				State: models.ExecutionStateFailed, FailureDetail: "recorded child failure",
			},
		},
	}
	first, err := fixture.engine.finishExecution(t.Context(), final)
	require.NoError(t, err)
	assert.Equal(t, models.AgentRunStateFailed, first.Output.Finish.Child.State)
	assert.Equal(t, AgentRunStateFailed, shared.actor.run.Snapshot().State)
	reused, err := fixture.engine.finishExecution(t.Context(), final)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	assert.Equal(t, 1, delivered)
	select {
	case <-shared.actor.run.Done():
	default:
		t.Fatal("failed child has an open completion channel")
	}
}
