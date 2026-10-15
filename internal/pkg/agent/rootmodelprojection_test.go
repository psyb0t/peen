package agent

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/essessey"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rootModelProjectionGuard struct {
	*session.Store
	checked             int
	beforeFailure       bool
	lostAcknowledgement bool
	pause               bool
}

func (s *rootModelProjectionGuard) CompleteExecutionActivity(
	ctx context.Context,
	sessionID uuid.UUID,
	lease session.ActivityLease,
	input session.CompleteActivityInput,
) (*models.ExecutionActivity, error) {
	page, err := s.ListMessages(ctx, sessionID, session.ListMessagesOptions{})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read root model pre-commit history")
	}
	for _, message := range page.Items {
		if message.Role == models.MessageRoleAssistant {
			return nil, ctxerrors.Wrap(commerr.ErrConflict,
				"root assistant committed before model result")
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
		return nil, ctxerrors.Wrap(err, "commit guarded root model")
	}
	if s.lostAcknowledgement {
		s.lostAcknowledgement = false

		return nil, ctxerrors.Wrap(commerr.ErrConflict, "model acknowledgement lost")
	}
	if s.pause {
		s.pause = false
		if _, err := s.PauseExecution(ctx, sessionID, activity.ExecutionID,
			"retain committed root model"); err != nil {
			return nil, ctxerrors.Wrap(err, "pause committed root model")
		}
	}

	return activity, nil
}

func rootModelProjectionFixture(
	t *testing.T,
	turns ...elelemtest.Turn,
) (executionLoopFixture, *rootWorkflowCallbacks, *rootCallbackScope) {
	t.Helper()
	fixture, _ := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver(turns...))
	callbacks := newRootWorkflowCallbacks(fixture.base.runtime,
		fixture.engine.options.GenerationID, nil)
	callbacks.bind(&fixture.engine.options)
	shared, err := callbacks.resolve(t.Context(), fixture.input)
	require.NoError(t, err)
	t.Cleanup(func() {
		fixture.base.runtime.closeActiveUserMessageQueue(shared.prepared)
	})

	return fixture, callbacks, shared
}

func TestRootModelProjectionCommitsAssistantWithResult(t *testing.T) {
	t.Parallel()
	fixture, _, shared := rootModelProjectionFixture(t,
		elelemtest.Text("committed root response"))
	guard := &rootModelProjectionGuard{Store: fixture.base.store}
	fixture.engine.options.Store = guard
	input := workflowRoundInput{Execution: fixture.input}
	first, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, "committed root response", first.Output.Text)
	assert.Equal(t, 1, guard.checked)
	reused, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	assert.Equal(t, first.Output, reused.Output)
	page, err := fixture.base.store.ListMessages(t.Context(),
		fixture.input.SessionID, session.ListMessagesOptions{})
	require.NoError(t, err)
	assistantCount := 0
	for _, message := range page.Items {
		if message.Role == models.MessageRoleAssistant {
			assistantCount++
			assert.Equal(t, first.Output.Text, message.Content)
		}
	}
	assert.Equal(t, 1, assistantCount)
	assert.True(t, shared.prepared.turn.pendingTranscript().isEmpty())
}

func TestRootModelProjectionRetriesUncommittedAssistant(t *testing.T) {
	t.Parallel()
	fixture, _, _ := rootModelProjectionFixture(t,
		elelemtest.Text("uncommitted root response"),
		elelemtest.Text("retry root response"))
	guard := &rootModelProjectionGuard{
		Store: fixture.base.store, beforeFailure: true,
	}
	fixture.engine.options.Store = guard
	input := workflowRoundInput{Execution: fixture.input}
	_, err := fixture.engine.modelRound(t.Context(), input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	page, err := fixture.base.store.ListMessages(t.Context(),
		fixture.input.SessionID, session.ListMessagesOptions{})
	require.NoError(t, err)
	for _, message := range page.Items {
		assert.NotEqual(t, models.MessageRoleAssistant, message.Role)
	}
	events, err := fixture.base.store.ListEvents(t.Context(),
		fixture.input.SessionID, session.ListEventsOptions{
			EventTypes: []string{essessey.EventTypeContentBlockDelta},
		})
	require.NoError(t, err)
	assert.NotEmpty(t, events.Items)
	result, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, "retry root response", result.Output.Text)
	assert.Equal(t, 2, guard.checked)
	runs, err := fixture.base.store.ListModelRuns(t.Context(),
		fixture.input.SessionID, session.ListModelRunsOptions{})
	require.NoError(t, err)
	assert.Len(t, runs.Items, 2)
}

func TestRootModelProjectionReusesLostAcknowledgement(t *testing.T) {
	t.Parallel()
	fixture, _, shared := rootModelProjectionFixture(t,
		elelemtest.Text("retained root response"))
	guard := &rootModelProjectionGuard{
		Store: fixture.base.store, lostAcknowledgement: true,
	}
	fixture.engine.options.Store = guard
	input := workflowRoundInput{Execution: fixture.input}
	_, err := fixture.engine.modelRound(t.Context(), input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	for range 2 {
		reused, err := fixture.engine.modelRound(t.Context(), input)
		require.NoError(t, err)
		assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	}
	assert.Equal(t, 1, guard.checked)
	assistantCount := 0
	for _, message := range shared.prepared.turn.messages {
		if message.Role == models.MessageRoleAssistant {
			assistantCount++
		}
	}
	assert.Equal(t, 1, assistantCount)
	assert.True(t, shared.prepared.turn.pendingTranscript().isEmpty())
	runs, err := fixture.base.store.ListModelRuns(t.Context(),
		fixture.input.SessionID, session.ListModelRunsOptions{})
	require.NoError(t, err)
	assert.Len(t, runs.Items, 1)
}

func TestRootModelProjectionPauseRetainsCommittedResult(t *testing.T) {
	t.Parallel()
	fixture, callbacks, _ := rootModelProjectionFixture(t,
		elelemtest.Text("paused root response"))
	guard := &rootModelProjectionGuard{
		Store: fixture.base.store, pause: true,
	}
	fixture.engine.options.Store = guard
	input := workflowRoundInput{Execution: fixture.input}
	paused, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionPaused, paused.Decision)
	_, err = fixture.base.store.ResumeExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	reused, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	assert.Equal(t, paused.Output, reused.Output)
	assert.Equal(t, 1, guard.checked)
	changed := reused.Output
	changed.Text = "changed root response"
	require.ErrorIs(t, callbacks.afterModelRound(t.Context(), input, changed),
		commerr.ErrConflict)
}

func TestRootModelProjectionReusesAfterSQLiteReopen(t *testing.T) {
	t.Parallel()
	fixture, _, _ := rootModelProjectionFixture(t,
		elelemtest.Text("root response before restart"))
	input := workflowRoundInput{Execution: fixture.input}
	first, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	before, err := fixture.base.store.ListMessages(t.Context(),
		fixture.input.SessionID, session.ListMessagesOptions{})
	require.NoError(t, err)
	fixture.engine.options.GenerationID = uuid.New()
	store, replacement := reopenModelProjectionRuntime(t, fixture, input)
	fixture.engine.options.Store = store
	callbacks := newRootWorkflowCallbacks(replacement,
		fixture.engine.options.GenerationID, nil)
	callbacks.bind(&fixture.engine.options)
	for range 2 {
		reused, err := fixture.engine.modelRound(t.Context(), input)
		require.NoError(t, err)
		assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
		assert.Equal(t, first.Output, reused.Output)
	}
	after, err := store.ListMessages(t.Context(), fixture.input.SessionID,
		session.ListMessagesOptions{})
	require.NoError(t, err)
	assert.Equal(t, before, after)
	runs, err := store.ListModelRuns(t.Context(), fixture.input.SessionID,
		session.ListModelRunsOptions{})
	require.NoError(t, err)
	assert.Len(t, runs.Items, 1)
}

func TestRootModelProjectionRequiresItsWorkerLease(t *testing.T) {
	t.Parallel()
	fixture, callbacks, shared := rootModelProjectionFixture(t)
	input := workflowRoundInput{Execution: fixture.input}
	_, _, err := callbacks.rootModelProjectionTurn(t.Context(), shared, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	ctx := context.WithValue(t.Context(), workflowActivityLeaseContextKey{},
		session.ActivityLease{ID: uuid.New(), GenerationID: uuid.New()})
	_, _, err = callbacks.rootModelProjectionTurn(ctx, shared, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
}
