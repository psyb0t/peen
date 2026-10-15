package session

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutionCreationReturnsStoredTimestamp(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	stored, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, stored, fixture.root)
}

const (
	executionTestVersion     = "testExecutionV1"
	executionTestTask        = "inspect example files"
	executionTestContext     = `{"messages":[{"text":"saved progress"}]}`
	executionTestPauseReason = "leave this work paused"
)

type executionFixture struct {
	store     *Store
	handle    *db.Handle
	directory string
	sessionID uuid.UUID
	turnID    uuid.UUID
	root      *models.Execution
}

func newExecutionFixture(t *testing.T) executionFixture {
	t.Helper()
	directory := t.TempDir()
	handle, err := db.Open(t.Context(), db.Config{Directory: directory})
	require.NoError(t, err)
	store := newTestStore(t, handle)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	session := newTestSession(t.Context(), t, store)
	lease := startAgentRunTestTurn(t.Context(), t, store, session.ID)
	input := executionTestInput(lease.TurnID)
	root, err := store.CreateExecution(t.Context(), session.ID, input)
	require.NoError(t, err)

	return executionFixture{
		store: store, handle: handle, directory: directory,
		sessionID: session.ID, turnID: lease.TurnID, root: root,
	}
}

func executionTestInput(turnID uuid.UUID) CreateExecutionInput {
	return CreateExecutionInput{
		ID: uuid.New(), RootTurnID: turnID, WorkflowVersion: executionTestVersion,
		Task: executionTestTask, WorkingDirectory: testWorkspace,
		Context: json.RawMessage(executionTestContext),
	}
}

func TestCreateExecutionPreservesPausedIdentity(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	_, err := fixture.store.PauseExecution(t.Context(),
		fixture.sessionID, fixture.root.ID, executionTestPauseReason)
	require.NoError(t, err)
	input := executionTestInput(fixture.turnID)
	input.ID = fixture.root.ID
	duplicate, err := fixture.store.CreateExecution(t.Context(),
		fixture.sessionID, input)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, duplicate.State)
	assert.EqualValues(t, 2, duplicate.EventCount)
	input.Task = "different work"
	_, err = fixture.store.CreateExecution(t.Context(), fixture.sessionID, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestCreateExecutionValidatesInput(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	testCases := []struct {
		name   string
		change func(*CreateExecutionInput)
	}{
		{name: "identity", change: func(value *CreateExecutionInput) {
			value.ID = uuid.Nil
		}},
		{name: "turn", change: func(value *CreateExecutionInput) {
			value.RootTurnID = uuid.Nil
		}},
		{name: "version", change: func(value *CreateExecutionInput) {
			value.WorkflowVersion = ""
		}},
		{name: "directory", change: func(value *CreateExecutionInput) {
			value.WorkingDirectory = ""
		}},
		{name: "missing context", change: func(value *CreateExecutionInput) {
			value.Context = nil
		}},
		{name: "null context", change: func(value *CreateExecutionInput) {
			value.Context = json.RawMessage(`null`)
		}},
		{name: "array context", change: func(value *CreateExecutionInput) {
			value.Context = json.RawMessage(`[]`)
		}},
		{name: "malformed context", change: func(value *CreateExecutionInput) {
			value.Context = json.RawMessage(`{"messages":`)
		}},
		{name: "root child run", change: func(value *CreateExecutionInput) {
			id := uuid.New()
			value.AgentRunID = &id
		}},
		{name: "child missing run", change: func(value *CreateExecutionInput) {
			value.ParentExecutionID = &fixture.root.ID
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := executionTestInput(fixture.turnID)
			tc.change(&input)
			_, err := fixture.store.CreateExecution(t.Context(),
				fixture.sessionID, input)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}

func TestExecutionOwnership(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	other := newTestSession(t.Context(), t, fixture.store)
	_, err := fixture.store.GetExecution(t.Context(), other.ID, fixture.root.ID)
	require.ErrorIs(t, err, commerr.ErrNotFound)
	_, err = fixture.store.CreateExecution(t.Context(), other.ID,
		executionTestInput(fixture.turnID))
	require.ErrorIs(t, err, commerr.ErrNotFound)
	_, err = fixture.store.PauseExecution(t.Context(),
		other.ID, fixture.root.ID, executionTestPauseReason)
	require.ErrorIs(t, err, commerr.ErrNotFound)
	_, err = fixture.store.ResumeExecution(t.Context(), other.ID, fixture.root.ID)
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

func TestCreateExecutionRetainsImmutableSDKIdentity(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	other := newTestSession(t.Context(), t, fixture.store)
	lease := startAgentRunTestTurn(t.Context(), t, fixture.store, other.ID)
	input := executionTestInput(lease.TurnID)
	input.WorkflowExecutionID = uuid.NewString()
	first, err := fixture.store.CreateExecution(t.Context(), other.ID, input)
	require.NoError(t, err)
	assert.Equal(t, input.WorkflowExecutionID, first.WorkflowExecutionID)
	repeated, err := fixture.store.CreateExecution(t.Context(), other.ID, input)
	require.NoError(t, err)
	assert.Equal(t, first, repeated)
	input.WorkflowExecutionID = uuid.NewString()
	_, err = fixture.store.CreateExecution(t.Context(), other.ID, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	stored, err := fixture.store.GetExecution(t.Context(), other.ID, first.ID)
	require.NoError(t, err)
	assert.Equal(t, first, stored)
}
