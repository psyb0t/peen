package session

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func jobWakeNotice(t *testing.T, fixture executionFixture,
	jobID uuid.UUID,
) *models.SessionNotice {
	t.Helper()
	data, err := json.Marshal(events.JobNoticeData{JobID: jobID})
	require.NoError(t, err)
	notice, err := fixture.store.CreateSessionNotice(t.Context(), fixture.sessionID,
		CreateSessionNoticeInput{
			Type: events.TypeJobExited, Source: events.JobSource,
			Summary: "job completed", DataJSON: string(data),
			Delivery: models.NoticeDeliveryWake,
		})
	require.NoError(t, err)

	return notice
}

func TestJobWakePausedChildAfterRootCompletionSurvivesReopen(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	job, err := fixture.store.CreateJob(t.Context(), fixture.sessionID,
		ownedJobInput(fixture.turnID, child.AgentRunID))
	require.NoError(t, err)
	notice := jobWakeNotice(t, fixture, job.ID)
	fixture.store.ReleaseTurn(inboxTestLease(t, fixture))
	executions := fixture.store.query.Execution
	_, err = executions.WithContext(t.Context()).Where(
		executions.ID.Eq(fixture.root.ID),
	).UpdateSimple(executions.State.Value(string(models.ExecutionStateCompleted)))
	require.NoError(t, err)
	_, err = fixture.store.PauseExecution(t.Context(), fixture.sessionID,
		child.ID, executionTestPauseReason)
	require.NoError(t, err)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store := newTestStore(t, handle)
	input := StartTurnInput{
		RequestID: uuid.New(), Workspace: testWorkspace,
		NoticeIDs: []uuid.UUID{notice.ID}, WakeNoticeID: &notice.ID,
	}
	_, err = store.AcquireTurn(t.Context(), fixture.sessionID, input)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	assert.False(t, store.IsActive(fixture.sessionID))
	pending, err := store.ListSessionNotices(t.Context(), fixture.sessionID,
		ListSessionNoticesOptions{State: models.NoticeStatePending})
	require.NoError(t, err)
	require.Len(t, pending.Items, 1)
	assert.Equal(t, notice.ID, pending.Items[0].ID)
	_, err = store.ResumeExecution(t.Context(), fixture.sessionID, child.ID)
	require.NoError(t, err)
	lease, err := store.AcquireTurn(t.Context(), fixture.sessionID, input)
	require.NoError(t, err)
	store.ReleaseTurn(lease)
}

func TestJobWakeChildAndRootIndependence(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name                string
		childJob, cancelled bool
		wantErr             error
	}{
		{name: "root under paused sibling"},
		{name: "child under paused sibling", childJob: true},
		{
			name: "cancelled child", childJob: true, cancelled: true,
			wantErr: commerr.ErrInvalidState,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionFixture(t)
			source := executionChildForTest(t, fixture, fixture.root)
			sibling := executionChildForTest(t, fixture, fixture.root)
			_, err := fixture.store.PauseExecution(t.Context(), fixture.sessionID,
				sibling.ID, executionTestPauseReason)
			require.NoError(t, err)
			var owner *uuid.UUID
			if tc.childJob {
				owner = source.AgentRunID
			}
			job, err := fixture.store.CreateJob(t.Context(), fixture.sessionID,
				ownedJobInput(fixture.turnID, owner))
			require.NoError(t, err)
			if tc.cancelled {
				_, _, err = fixture.store.RequestAgentRunCancellation(t.Context(),
					fixture.sessionID, *source.AgentRunID)
				require.NoError(t, err)
			}
			notice := jobWakeNotice(t, fixture, job.ID)
			fixture.store.ReleaseTurn(inboxTestLease(t, fixture))
			lease, err := fixture.store.AcquireTurn(t.Context(), fixture.sessionID,
				StartTurnInput{
					RequestID: uuid.New(), Workspace: testWorkspace,
					NoticeIDs: []uuid.UUID{notice.ID}, WakeNoticeID: &notice.ID,
				})
			require.ErrorIs(t, err, tc.wantErr)
			if tc.wantErr == nil {
				fixture.store.ReleaseTurn(lease)
			}
		})
	}
}

func TestJobWakeRejectsInvalidPayload(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name, data string
		wantErr    error
	}{
		{name: "array", data: `[]`, wantErr: commerr.ErrValidationFailed},
		{name: "null", data: `null`, wantErr: commerr.ErrValidationFailed},
		{name: "empty", data: `{}`, wantErr: commerr.ErrValidationFailed},
		{
			name: "malformed", data: `{"jobId":"wrong"}`,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "missing", data: `{"jobId":"11111111-1111-4111-8111-111111111111"}`,
			wantErr: commerr.ErrNotFound,
		},
		{name: "foreign", wantErr: commerr.ErrNotFound},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionFixture(t)
			job := createJobForTest(t.Context(), t, fixture.store,
				fixture.sessionID, fixture.turnID, 0)
			notice := jobWakeNotice(t, fixture, job.ID)
			data := tc.data
			if tc.name == "foreign" {
				foreign := newTestSession(t.Context(), t, fixture.store)
				lease := startAgentRunTestTurn(t.Context(), t, fixture.store, foreign.ID)
				other := createJobForTest(t.Context(), t, fixture.store,
					foreign.ID, lease.TurnID, 0)
				fixture.store.ReleaseTurn(lease)
				encoded, err := json.Marshal(events.JobNoticeData{JobID: other.ID})
				require.NoError(t, err)
				data = string(encoded)
			}
			notices := fixture.store.query.SessionNotice
			_, err := notices.WithContext(t.Context()).Where(notices.ID.Eq(notice.ID)).
				UpdateSimple(notices.DataJSON.Value(data))
			require.NoError(t, err)
			fixture.store.ReleaseTurn(inboxTestLease(t, fixture))
			_, err = fixture.store.AcquireTurn(t.Context(), fixture.sessionID,
				StartTurnInput{
					RequestID: uuid.New(), Workspace: testWorkspace,
					NoticeIDs: []uuid.UUID{notice.ID}, WakeNoticeID: &notice.ID,
				})
			require.ErrorIs(t, err, tc.wantErr)
			assert.False(t, fixture.store.IsActive(fixture.sessionID))
		})
	}
}
