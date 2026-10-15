package session

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func ownedJobInput(turnID uuid.UUID, owner *uuid.UUID) CreateJobInput {
	return CreateJobInput{
		ID: uuid.New(), TurnID: turnID, AgentRunID: owner,
		PID: 1, Purpose: "inspect files", Command: jobTestCommand,
		Directory: testWorkspace,
	}
}

func TestJobOwnerSurvivesSQLiteReopen(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	job, err := fixture.store.CreateJob(t.Context(), fixture.sessionID,
		ownedJobInput(fixture.turnID, child.AgentRunID))
	require.NoError(t, err)
	assert.Equal(t, child.AgentRunID, job.AgentRunID)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store := newTestStore(t, handle)
	restored, err := store.GetJob(t.Context(), fixture.sessionID, job.ID)
	require.NoError(t, err)
	assert.Equal(t, job.AgentRunID, restored.AgentRunID)
	assert.Equal(t, job.TurnID, restored.TurnID)
}

func TestJobOwnerValidation(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		wantErr error
	}{
		{name: "root"},
		{name: "child"},
		{name: "zero", wantErr: commerr.ErrValidationFailed},
		{name: "missing", wantErr: commerr.ErrNotFound},
		{name: "foreign", wantErr: commerr.ErrNotFound},
		{name: "wrong turn", wantErr: commerr.ErrConflict},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionFixture(t)
			child := executionChildForTest(t, fixture, fixture.root)
			owner := child.AgentRunID
			fixture.store.ReleaseTurn(inboxTestLease(t, fixture))
			turnID := fixture.turnID
			switch tc.name {
			case "root":
				owner = nil
			case "child":
			case "zero":
				owner = &uuid.Nil
			case "missing":
				id := uuid.New()
				owner = &id
			case "foreign":
				foreign := newTestSession(t.Context(), t, fixture.store)
				lease := startAgentRunTestTurn(t.Context(), t, fixture.store, foreign.ID)
				run := createAgentRunForTest(t.Context(), t, fixture.store, foreign.ID,
					lease.TurnID, 1)
				fixture.store.ReleaseTurn(lease)
				owner = &run.ID
			case "wrong turn":
				lease := startAgentRunTestTurn(t.Context(), t, fixture.store,
					fixture.sessionID)
				turnID = lease.TurnID
				fixture.store.ReleaseTurn(lease)
			}
			job, err := fixture.store.CreateJob(t.Context(), fixture.sessionID,
				ownedJobInput(turnID, owner))
			require.ErrorIs(t, err, tc.wantErr)
			if tc.wantErr == nil {
				assert.Equal(t, owner, job.AgentRunID)

				return
			}
			count, err := fixture.store.query.Job.WithContext(t.Context()).Count()
			require.NoError(t, err)
			assert.Zero(t, count)
		})
	}
}

func TestJobOwnerForeignKeyRejectsOtherSession(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	foreign := newTestSession(t.Context(), t, fixture.store)
	lease := startAgentRunTestTurn(t.Context(), t, fixture.store, foreign.ID)
	fixture.store.ReleaseTurn(lease)
	err := fixture.store.query.Job.WithContext(t.Context()).Create(&models.Job{
		ID: uuid.New(), SessionID: foreign.ID, TurnID: lease.TurnID,
		AgentRunID: child.AgentRunID, PID: 1, Purpose: "inspect files",
		Command: jobTestCommand, Directory: testWorkspace,
		State: models.JobStateRunning, StartedAt: fixture.store.now(),
	})
	require.ErrorIs(t, err, gorm.ErrForeignKeyViolated)
}
