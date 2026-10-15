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
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type childModelProjectionGuard struct {
	*session.Store
	runID               uuid.UUID
	checked             int
	beforeFailure       bool
	lostAcknowledgement bool
	pause               bool
}

func (s *childModelProjectionGuard) CompleteExecutionActivity(
	ctx context.Context,
	sessionID uuid.UUID,
	lease session.ActivityLease,
	input session.CompleteActivityInput,
) (*models.ExecutionActivity, error) {
	history, err := s.AgentRunHistory(ctx, sessionID, s.runID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read child model pre-commit history")
	}
	for _, message := range history.Messages {
		if message.Role == models.MessageRoleAssistant {
			return nil, ctxerrors.Wrap(commerr.ErrConflict,
				"child assistant committed before model result")
		}
	}
	s.checked++

	if s.beforeFailure {
		s.beforeFailure = false

		return nil, ctxerrors.Wrap(commerr.ErrConflict, "model commit unavailable")
	}

	activity, err := s.Store.CompleteExecutionActivity(ctx, sessionID,
		lease, input)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "commit guarded child model")
	}
	if s.lostAcknowledgement {
		s.lostAcknowledgement = false

		return nil, ctxerrors.Wrap(commerr.ErrConflict, "model acknowledgement lost")
	}
	if s.pause {
		s.pause = false
		if _, err := s.PauseExecution(ctx, sessionID, activity.ExecutionID,
			"retain committed child model"); err != nil {
			return nil, ctxerrors.Wrap(err, "pause committed child model")
		}
	}

	return activity, nil
}

func TestChildModelProjectionCommitsAssistantWithResult(t *testing.T) {
	t.Parallel()
	fixture, input, original := childModelFixture(t, elelemtest.Text("committed child response"))
	delivered := 0
	dispatch := newWorkflowDispatch(t.Context(), fixture.base.runtime,
		fixture.engine.options.GenerationID, func(event Event) error {
			if event.Type == EventTypeAgentRunAssistantMessage {
				delivered++
			}

			return nil
		})
	dispatch.bind(&fixture.engine.options)
	guard := &childModelProjectionGuard{
		Store: fixture.base.store, runID: original.run.ID,
	}
	fixture.engine.options.Store = guard
	first, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, "committed child response", first.Output.Text)
	assert.Equal(t, 1, guard.checked)
	assert.Equal(t, 1, delivered)
	reused, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	assert.Equal(t, first.Output, reused.Output)
	assert.Equal(t, 1, delivered)
	history, err := fixture.base.store.AgentRunHistory(t.Context(),
		input.Execution.SessionID, original.run.ID)
	require.NoError(t, err)
	require.Len(t, history.Messages, 2)
	assert.Equal(t, first.Output.Text, history.Messages[1].Content)
}

func childModelProjectionFixture(
	t *testing.T,
	sink EventSink,
	turns ...elelemtest.Turn,
) (executionLoopFixture, workflowRoundInput, uuid.UUID, *workflowDispatch) {
	t.Helper()
	fixture, input, original := childModelFixture(t, turns...)
	dispatch := newWorkflowDispatch(t.Context(), fixture.base.runtime,
		fixture.engine.options.GenerationID, sink)
	dispatch.bind(&fixture.engine.options)

	return fixture, input, original.run.ID, dispatch
}

func TestChildModelProjectionRetriesCommittedDelivery(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name                string
		lostAcknowledgement bool
		failedSink          bool
	}{
		{name: "lost acknowledgement", lostAcknowledgement: true},
		{name: "failed sink", failedSink: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			delivered := 0
			fixture, input, runID, _ := childModelProjectionFixture(t,
				func(event Event) error {
					if event.Type != EventTypeAgentRunAssistantMessage {
						return nil
					}
					if tc.failedSink {
						tc.failedSink = false

						return ctxerrors.Wrap(commerr.ErrConflict, "sink unavailable")
					}
					delivered++

					return nil
				}, elelemtest.Text("retained child response"))
			guard := &childModelProjectionGuard{
				Store: fixture.base.store, runID: runID,
				lostAcknowledgement: tc.lostAcknowledgement,
			}
			fixture.engine.options.Store = guard
			_, err := fixture.engine.modelRound(t.Context(), input)
			require.ErrorIs(t, err, commerr.ErrConflict)
			for range 2 {
				reused, err := fixture.engine.modelRound(t.Context(), input)
				require.NoError(t, err)
				assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
			}
			assert.Equal(t, 1, delivered)
			assert.Equal(t, 1, guard.checked)
			runs, err := fixture.base.store.ListModelRuns(t.Context(),
				input.Execution.SessionID, session.ListModelRunsOptions{})
			require.NoError(t, err)
			assert.Len(t, runs.Items, 1)
		})
	}
}

func TestChildModelProjectionRetriesUncommittedAssistant(t *testing.T) {
	t.Parallel()
	fixture, input, runID, _ := childModelProjectionFixture(t, nil,
		elelemtest.Text("uncommitted child response"),
		elelemtest.Text("retry child response"))
	guard := &childModelProjectionGuard{
		Store: fixture.base.store, runID: runID, beforeFailure: true,
	}
	fixture.engine.options.Store = guard
	_, err := fixture.engine.modelRound(t.Context(), input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	history, err := fixture.base.store.AgentRunHistory(t.Context(),
		input.Execution.SessionID, runID)
	require.NoError(t, err)
	require.Len(t, history.Messages, 1)
	page, err := fixture.base.store.ListAgentRunEvents(t.Context(),
		input.Execution.SessionID, runID, session.ListAgentRunEventsOptions{})
	require.NoError(t, err)
	hasText := false
	for _, event := range page.Items {
		assert.NotEqual(t, EventTypeAgentRunAssistantMessage, event.EventType)
		hasText = hasText || event.EventType == EventTypeAgentRunTextDelta
	}
	assert.True(t, hasText)
	result, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, "retry child response", result.Output.Text)
	history, err = fixture.base.store.AgentRunHistory(t.Context(),
		input.Execution.SessionID, runID)
	require.NoError(t, err)
	require.Len(t, history.Messages, 2)
	assert.Equal(t, result.Output.Text, history.Messages[1].Content)
	runs, err := fixture.base.store.ListModelRuns(t.Context(),
		input.Execution.SessionID, session.ListModelRunsOptions{})
	require.NoError(t, err)
	assert.Len(t, runs.Items, 2)
}

func TestChildModelProjectionPauseRetainsCommittedResult(t *testing.T) {
	t.Parallel()
	fixture, input, runID, dispatch := childModelProjectionFixture(t, nil,
		elelemtest.Text("paused child response"))
	guard := &childModelProjectionGuard{
		Store: fixture.base.store, runID: runID, pause: true,
	}
	fixture.engine.options.Store = guard
	paused, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionPaused, paused.Decision)
	_, err = fixture.base.store.ResumeExecution(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID)
	require.NoError(t, err)
	reused, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	assert.Equal(t, paused.Output, reused.Output)
	assert.Equal(t, 1, guard.checked)
	changed := reused.Output
	changed.Text = "changed response"
	require.ErrorIs(t, dispatch.child.afterModelRound(t.Context(), input,
		changed), commerr.ErrConflict)
}

func reopenModelProjectionRuntime(
	t *testing.T,
	fixture executionLoopFixture,
	input workflowRoundInput,
) (*session.Store, *Runtime) {
	t.Helper()
	require.NoError(t, fixture.base.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{
		Directory: fixture.base.stateDirectory,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)
	_, err = store.CreateWorkerGeneration(t.Context(), input.Execution.SessionID,
		session.CreateWorkerGenerationInput{
			ID:   fixture.engine.options.GenerationID,
			Kind: string(worker.KindControl), State: string(worker.StateReady),
			AgentSpace: string(worker.KindControl), Workspace: fixture.base.workspace,
			CredentialHash: "EXAMPLE-DO-NOT-USE",
		})
	require.NoError(t, err)
	original := fixture.base.runtime
	replacement, err := NewRuntime(t.Context(), RuntimeOptions{
		Store: store, Resolver: original.resolver, Models: original.models,
		RootAgent: original.rootAgent, DefaultModel: original.defaultModel,
		StartupSessionID:   input.Execution.SessionID,
		MaxContextTokens:   original.maxContextTokens,
		TurnTimeout:        original.turnTimeout,
		WorkerGenerationID: fixture.engine.options.GenerationID,
		ConfigDirectory:    fixture.base.configDirectory,
	})
	require.NoError(t, err)

	return store, replacement
}

func TestChildModelProjectionReusesAfterSQLiteReopen(t *testing.T) {
	t.Parallel()
	fixture, input, runID, _ := childModelProjectionFixture(t, nil,
		elelemtest.Text("child response before restart"))
	first, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	before, err := fixture.base.store.ListAgentRunEvents(t.Context(),
		input.Execution.SessionID, runID, session.ListAgentRunEventsOptions{})
	require.NoError(t, err)
	fixture.engine.options.GenerationID = uuid.New()
	store, replacement := reopenModelProjectionRuntime(t, fixture, input)
	fixture.engine.options.Store = store
	delivered := 0
	dispatch := newWorkflowDispatch(t.Context(), replacement,
		fixture.engine.options.GenerationID, func(event Event) error {
			if event.Type == EventTypeAgentRunAssistantMessage {
				delivered++
			}

			return nil
		})
	dispatch.bind(&fixture.engine.options)
	for range 2 {
		reused, err := fixture.engine.modelRound(t.Context(), input)
		require.NoError(t, err)
		assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
		assert.Equal(t, first.Output, reused.Output)
	}
	assert.Equal(t, 1, delivered)
	after, err := store.ListAgentRunEvents(t.Context(), input.Execution.SessionID,
		runID, session.ListAgentRunEventsOptions{})
	require.NoError(t, err)
	assert.Equal(t, before, after)
	runs, err := store.ListModelRuns(t.Context(), input.Execution.SessionID,
		session.ListModelRunsOptions{})
	require.NoError(t, err)
	assert.Len(t, runs.Items, 1)
}

func TestChildModelProjectionRequiresItsWorkerLease(t *testing.T) {
	t.Parallel()
	fixture, input, _, dispatch := childModelProjectionFixture(t, nil)
	shared, err := dispatch.child.resolve(t.Context(), input)
	require.NoError(t, err)
	_, err = dispatch.child.childModelProjectionSink(t.Context(), shared, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	ctx := context.WithValue(t.Context(), workflowActivityLeaseContextKey{},
		session.ActivityLease{ID: uuid.New(), GenerationID: uuid.New()})
	_, err = dispatch.child.childModelProjectionSink(ctx, shared, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	assert.NotEqual(t, uuid.Nil, fixture.engine.options.GenerationID)
}
