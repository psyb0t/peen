package session

import (
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutionControlRevisionRefusesOverflow(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	repository := fixture.store.query.Execution
	_, err := repository.WithContext(t.Context()).
		Where(repository.ID.Eq(fixture.root.ID)).
		UpdateSimple(repository.ControlRevision.Value(math.MaxInt64))
	require.NoError(t, err)
	_, err = fixture.store.PauseExecution(t.Context(), fixture.sessionID,
		fixture.root.ID, executionTestPauseReason)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	stored, err := fixture.store.GetExecution(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(math.MaxInt64), stored.ControlRevision)
	assert.Equal(t, models.ExecutionStateQueued, stored.State)
}

func executionChildForTest(
	t *testing.T,
	fixture executionFixture,
	parent *models.Execution,
) *models.Execution {
	t.Helper()
	run, err := fixture.store.CreateAgentRun(t.Context(), fixture.sessionID,
		StartAgentRunInput{
			ID: uuid.New(), ParentTurnID: fixture.turnID,
			ParentAgentRunID: parent.AgentRunID,
			RequestID:        uuid.New(), Name: "exampleChild",
			Definition: models.AgentRunDefinitionAdHoc, Depth: 1,
			Workspace: testWorkspace, ModelReference: agentRunTestModel,
			ModelID: agentRunTestModel, Task: executionTestTask,
			Instructions: agentRunTestInstructions, AllowedToolsJSON: `[]`,
			SystemPrompt: agentRunTestPrompt,
		})
	require.NoError(t, err)
	input := executionTestInput(fixture.turnID)
	input.AgentRunID = &run.ID
	input.ParentExecutionID = &parent.ID
	child, err := fixture.store.CreateExecution(t.Context(),
		fixture.sessionID, input)
	require.NoError(t, err)

	return child
}

func TestExecutionPauseSurvivesReopenAndExplicitResume(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	grandchild := executionChildForTest(t, fixture, child)
	changed, err := fixture.store.PauseExecution(t.Context(),
		fixture.sessionID, fixture.root.ID, executionTestPauseReason)
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{fixture.root.ID, child.ID, grandchild.ID},
		changed)
	changed, err = fixture.store.PauseExecution(t.Context(),
		fixture.sessionID, fixture.root.ID, executionTestPauseReason)
	require.NoError(t, err)
	assert.Empty(t, changed)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	reopened, err := NewStore(handle, Options{})
	require.NoError(t, err)
	_, err = reopened.RecoverInterrupted(t.Context())
	require.NoError(t, err)
	lease, err := reopened.AcquireTurn(t.Context(), fixture.sessionID,
		StartTurnInput{RequestID: uuid.New(), Workspace: testWorkspace})
	require.NoError(t, err)
	unrelated, err := reopened.CreateExecution(t.Context(), fixture.sessionID,
		executionTestInput(lease.TurnID))
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateQueued, unrelated.State)
	for _, id := range []uuid.UUID{fixture.root.ID, child.ID, grandchild.ID} {
		stored, err := reopened.GetExecution(t.Context(), fixture.sessionID, id)
		require.NoError(t, err)
		assert.Equal(t, models.ExecutionStatePaused, stored.State)
		assert.Equal(t, executionTestPauseReason, stored.PauseReason)
		assert.NotNil(t, stored.PausedAt)
	}
	changed, err = reopened.ResumeExecution(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	assert.Len(t, changed, 3)
	stored, err := reopened.GetExecution(t.Context(),
		fixture.sessionID, grandchild.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateQueued, stored.State)
	assert.Nil(t, stored.PausedAt)
	history, err := reopened.ListExecutionEvents(t.Context(),
		fixture.sessionID, grandchild.ID, ListEventsOptions{})
	require.NoError(t, err)
	require.Len(t, history.Items, 3)
	assert.Equal(t, string(ExecutionEventTypePaused), history.Items[1].EventType)
	assert.Equal(t, string(ExecutionEventTypeResumed), history.Items[2].EventType)
}

func TestNewChildInheritsParentPause(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	_, err := fixture.store.PauseExecution(t.Context(),
		fixture.sessionID, fixture.root.ID, executionTestPauseReason)
	require.NoError(t, err)
	child := executionChildForTest(t, fixture, fixture.root)
	assert.Equal(t, models.ExecutionStatePaused, child.State)
	assert.Equal(t, executionTestPauseReason, child.PauseReason)
	assert.NotNil(t, child.PausedAt)
}

func TestExecutionPauseTargetsOnlyItsSubtree(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	grandchild := executionChildForTest(t, fixture, child)
	sibling := executionChildForTest(t, fixture, fixture.root)
	changed, err := fixture.store.PauseExecution(t.Context(),
		fixture.sessionID, child.ID, executionTestPauseReason)
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{child.ID, grandchild.ID}, changed)
	for _, id := range []uuid.UUID{fixture.root.ID, sibling.ID} {
		stored, err := fixture.store.GetExecution(t.Context(), fixture.sessionID, id)
		require.NoError(t, err)
		assert.Equal(t, models.ExecutionStateQueued, stored.State)
	}
}

func TestExecutionControlPreservesTerminalDescendants(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	for _, state := range []models.ExecutionState{
		models.ExecutionStateCompleted, models.ExecutionStateFailed,
	} {
		child := executionChildForTest(t, fixture, fixture.root)
		repository := fixture.store.query.Execution
		_, err := repository.WithContext(t.Context()).Where(
			repository.ID.Eq(child.ID),
		).UpdateSimple(repository.State.Value(string(state)))
		require.NoError(t, err)
	}
	changed, err := fixture.store.PauseExecution(t.Context(),
		fixture.sessionID, fixture.root.ID, executionTestPauseReason)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{fixture.root.ID}, changed)
	changed, err = fixture.store.ResumeExecution(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{fixture.root.ID}, changed)
	for _, state := range []models.ExecutionState{
		models.ExecutionStateCompleted, models.ExecutionStateFailed,
	} {
		page, err := fixture.store.SearchExecutions(t.Context(),
			fixture.sessionID, SearchExecutionsOptions{State: state})
		require.NoError(t, err)
		require.Len(t, page.Items, 1)
		assert.Equal(t, state, page.Items[0].State)
		assert.EqualValues(t, 1, page.Items[0].EventCount)
	}
}
