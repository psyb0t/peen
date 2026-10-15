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

func agentWakeFixture(t *testing.T) (executionFixture, *models.Execution,
	*models.SessionNotice,
) {
	t.Helper()
	fixture := newExecutionFixture(t)
	child := executionChildForTest(t, fixture, fixture.root)
	data, err := json.Marshal(events.AgentNoticeData{RunID: *child.AgentRunID})
	require.NoError(t, err)
	notice, err := fixture.store.CreateSessionNotice(t.Context(),
		fixture.sessionID, CreateSessionNoticeInput{
			Type: events.TypeAgentQuestion, Source: events.AgentSource,
			Summary: "child needs input", DataJSON: string(data),
			Delivery: models.NoticeDeliveryWake,
		})
	require.NoError(t, err)
	fixture.store.ReleaseTurn(inboxTestLease(t, fixture))

	return fixture, child, notice
}

func TestWakeAdmissionRefusesPausedChildAfterRootEnds(t *testing.T) {
	t.Parallel()
	fixture, child, notice := agentWakeFixture(t)
	repository := fixture.store.query.Execution
	_, err := repository.WithContext(t.Context()).Where(
		repository.ID.Eq(fixture.root.ID),
	).UpdateSimple(repository.State.Value(string(models.ExecutionStateCompleted)))
	require.NoError(t, err)
	_, err = fixture.store.PauseExecution(t.Context(), fixture.sessionID,
		child.ID, executionTestPauseReason)
	require.NoError(t, err)
	input := StartTurnInput{
		RequestID: uuid.New(), Workspace: testWorkspace,
		NoticeIDs: []uuid.UUID{notice.ID}, WakeNoticeID: &notice.ID,
		Messages: []MessageInput{{Role: models.MessageRoleUser, Content: "wake"}},
	}
	_, err = fixture.store.AcquireTurn(t.Context(), fixture.sessionID, input)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	assert.False(t, fixture.store.IsActive(fixture.sessionID))
	pending, err := fixture.store.ListSessionNotices(t.Context(),
		fixture.sessionID, ListSessionNoticesOptions{State: models.NoticeStatePending})
	require.NoError(t, err)
	require.Len(t, pending.Items, 1)
	assert.Equal(t, notice.ID, pending.Items[0].ID)
	turns, err := fixture.store.query.Turn.WithContext(t.Context()).Count()
	require.NoError(t, err)
	assert.EqualValues(t, 1, turns)
	_, err = fixture.store.ResumeExecution(t.Context(), fixture.sessionID, child.ID)
	require.NoError(t, err)
	lease, err := fixture.store.AcquireTurn(t.Context(), fixture.sessionID, input)
	require.NoError(t, err)
	fixture.store.ReleaseTurn(lease)
}

func TestWakeAdmissionValidatesNoticeSnapshot(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		change  func(*testing.T, *Store, uuid.UUID, *StartTurnInput)
		wantErr error
	}{
		{name: "owned", change: func(_ *testing.T, _ *Store,
			_ uuid.UUID, _ *StartTurnInput) {
		}},
		{
			name: "omitted source", change: func(_ *testing.T, _ *Store,
				_ uuid.UUID, input *StartTurnInput,
			) {
				input.NoticeIDs = nil
			},
			wantErr: commerr.ErrConflict,
		},
		{
			name: "zero source", change: func(_ *testing.T, _ *Store,
				_ uuid.UUID, input *StartTurnInput,
			) {
				input.WakeNoticeID = &uuid.Nil
			},
			wantErr: commerr.ErrValidationFailed,
		},
		{name: "missing source", change: func(_ *testing.T, _ *Store,
			_ uuid.UUID, input *StartTurnInput,
		) {
			id := uuid.New()
			input.WakeNoticeID = &id
		}, wantErr: commerr.ErrConflict},
		{
			name: "foreign source", change: foreignWakeNotice,
			wantErr: commerr.ErrNotFound,
		},
		{name: "consumed source", change: func(t *testing.T, store *Store,
			sessionID uuid.UUID, _ *StartTurnInput,
		) {
			t.Helper()
			_, err := store.DrainSessionNotices(t.Context(), sessionID)
			require.NoError(t, err)
		}, wantErr: commerr.ErrConflict},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, sessionID, noticeID := openingNoticeFixture(t)
			input := StartTurnInput{
				RequestID: uuid.New(), Workspace: testWorkspace,
				NoticeIDs: []uuid.UUID{noticeID}, WakeNoticeID: &noticeID,
			}
			tc.change(t, store, sessionID, &input)
			before, err := store.ListSessionNotices(t.Context(), sessionID,
				ListSessionNoticesOptions{})
			require.NoError(t, err)
			lease, err := store.AcquireTurn(t.Context(), sessionID, input)
			require.ErrorIs(t, err, tc.wantErr)
			if tc.wantErr == nil {
				store.ReleaseTurn(lease)

				return
			}
			assert.False(t, store.IsActive(sessionID))
			after, err := store.ListSessionNotices(t.Context(), sessionID,
				ListSessionNoticesOptions{})
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}

func foreignWakeNotice(t *testing.T, store *Store,
	_ uuid.UUID, input *StartTurnInput,
) {
	t.Helper()
	owner := newTestSession(t.Context(), t, store)
	notice, err := store.CreateSessionNotice(t.Context(), owner.ID,
		CreateSessionNoticeInput{
			Type: "app.ready", Source: "example",
			Summary: "foreign", DataJSON: `{}`, Delivery: models.NoticeDeliveryWake,
		})
	require.NoError(t, err)
	input.WakeNoticeID = &notice.ID
	input.NoticeIDs = append(input.NoticeIDs, notice.ID)
}

func TestAgentWakeAdmissionSourceStates(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name                  string
		runState              models.AgentRunState
		rootState             models.ExecutionState
		turnState             models.TurnState
		cancelRun, cancelTurn bool
		wantErr               error
	}{
		{
			name: "running", runState: models.AgentRunStateRunning,
			rootState: models.ExecutionStateRunning,
		},
		{
			name: "completed", runState: models.AgentRunStateCompleted,
			rootState: models.ExecutionStateCompleted,
		},
		{
			name: "failed", runState: models.AgentRunStateFailed,
			rootState: models.ExecutionStateFailed,
		},
		{
			name: "paused root", runState: models.AgentRunStateRunning,
			rootState: models.ExecutionStatePaused, wantErr: commerr.ErrInvalidState,
		},
		{
			name: "cancelled run", runState: models.AgentRunStateCancelled,
			rootState: models.ExecutionStateCompleted, wantErr: commerr.ErrInvalidState,
		},
		{
			name: "cancel requested run", runState: models.AgentRunStateCompleted,
			rootState: models.ExecutionStateCompleted, cancelRun: true,
			wantErr: commerr.ErrInvalidState,
		},
		{
			name: "cancelled turn", runState: models.AgentRunStateCompleted,
			rootState: models.ExecutionStateCompleted,
			turnState: models.TurnStateCancelled, wantErr: commerr.ErrInvalidState,
		},
		{
			name: "cancel requested turn", runState: models.AgentRunStateCompleted,
			rootState: models.ExecutionStateCompleted, cancelTurn: true,
			wantErr: commerr.ErrInvalidState,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, child, notice := agentWakeFixture(t)
			runs := fixture.store.query.AgentRun
			_, err := runs.WithContext(t.Context()).Where(runs.ID.Eq(*child.AgentRunID)).
				UpdateSimple(runs.State.Value(string(tc.runState)),
					runs.CancelRequested.Value(tc.cancelRun))
			require.NoError(t, err)
			executions := fixture.store.query.Execution
			_, err = executions.WithContext(t.Context()).Where(
				executions.ID.Eq(fixture.root.ID),
			).UpdateSimple(executions.State.Value(string(tc.rootState)))
			require.NoError(t, err)
			turns := fixture.store.query.Turn
			_, err = turns.WithContext(t.Context()).Where(turns.ID.Eq(fixture.turnID)).
				UpdateSimple(turns.CancelRequested.Value(tc.cancelTurn))
			require.NoError(t, err)
			if tc.turnState != "" {
				_, err = turns.WithContext(t.Context()).Where(
					turns.ID.Eq(fixture.turnID),
				).UpdateSimple(turns.State.Value(string(tc.turnState)))
				require.NoError(t, err)
			}

			lease, err := fixture.store.AcquireTurn(t.Context(), fixture.sessionID,
				StartTurnInput{
					RequestID: uuid.New(), Workspace: testWorkspace,
					NoticeIDs: []uuid.UUID{notice.ID}, WakeNoticeID: &notice.ID,
				})
			require.ErrorIs(t, err, tc.wantErr)
			if tc.wantErr == nil {
				fixture.store.ReleaseTurn(lease)

				return
			}
			assert.False(t, fixture.store.IsActive(fixture.sessionID))
			page, err := fixture.store.ListSessionNotices(t.Context(),
				fixture.sessionID, ListSessionNoticesOptions{})
			require.NoError(t, err)
			require.Len(t, page.Items, 1)
			assert.Equal(t, models.NoticeStatePending, page.Items[0].State)
		})
	}
}

func TestAgentWakeAdmissionPayload(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name, data string
		wantErr    error
	}{
		{name: "array", data: `[]`, wantErr: commerr.ErrValidationFailed},
		{name: "null", data: `null`, wantErr: commerr.ErrValidationFailed},
		{name: "missing run", data: `{}`, wantErr: commerr.ErrValidationFailed},
		{
			name: "malformed run", data: `{"runId":"wrong"}`,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "unknown run", data: `{"runId":"11111111-1111-4111-8111-111111111111"}`,
			wantErr: commerr.ErrNotFound,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, _, notice := agentWakeFixture(t)
			notices := fixture.store.query.SessionNotice
			_, err := notices.WithContext(t.Context()).Where(notices.ID.Eq(notice.ID)).
				UpdateSimple(notices.DataJSON.Value(tc.data))
			require.NoError(t, err)
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

func TestAgentWakeAdmissionChecksAncestors(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		configure func(*testing.T, executionFixture, *models.Execution,
			*models.Execution)
		wantErr error
	}{
		{name: "paused ancestor", configure: func(t *testing.T,
			fixture executionFixture, parent, child *models.Execution,
		) {
			t.Helper()
			_, err := fixture.store.PauseExecution(t.Context(), fixture.sessionID,
				parent.ID, executionTestPauseReason)
			require.NoError(t, err)
			_, err = fixture.store.ResumeExecution(t.Context(), fixture.sessionID,
				child.ID)
			require.NoError(t, err)
		}, wantErr: commerr.ErrInvalidState},
		{name: "cancelled ancestor run", configure: func(t *testing.T,
			fixture executionFixture, parent, _ *models.Execution,
		) {
			t.Helper()
			_, _, err := fixture.store.RequestAgentRunCancellation(t.Context(),
				fixture.sessionID, *parent.AgentRunID)
			require.NoError(t, err)
		}, wantErr: commerr.ErrInvalidState},
		{name: "run cycle", configure: func(t *testing.T,
			fixture executionFixture, parent, child *models.Execution,
		) {
			t.Helper()
			runs := fixture.store.query.AgentRun
			_, err := runs.WithContext(t.Context()).Where(
				runs.ID.Eq(*parent.AgentRunID),
			).UpdateSimple(runs.ParentAgentRunID.Value(*child.AgentRunID))
			require.NoError(t, err)
		}, wantErr: commerr.ErrConflict},
		{name: "execution cycle", configure: func(t *testing.T,
			fixture executionFixture, parent, child *models.Execution,
		) {
			t.Helper()
			executions := fixture.store.query.Execution
			_, err := executions.WithContext(t.Context()).Where(
				executions.ID.Eq(parent.ID),
			).UpdateSimple(executions.ParentExecutionID.Value(child.ID))
			require.NoError(t, err)
		}, wantErr: commerr.ErrConflict},
		{name: "unrelated pause", configure: func(t *testing.T,
			fixture executionFixture, _, _ *models.Execution,
		) {
			t.Helper()
			sibling := executionChildForTest(t, fixture, fixture.root)
			_, err := fixture.store.PauseExecution(t.Context(), fixture.sessionID,
				sibling.ID, executionTestPauseReason)
			require.NoError(t, err)
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, parent, notice := agentWakeFixture(t)
			child := executionChildForTest(t, fixture, parent)
			tc.configure(t, fixture, parent, child)
			data, err := json.Marshal(events.AgentNoticeData{RunID: *child.AgentRunID})
			require.NoError(t, err)
			notices := fixture.store.query.SessionNotice
			_, err = notices.WithContext(t.Context()).Where(notices.ID.Eq(notice.ID)).
				UpdateSimple(notices.DataJSON.Value(string(data)))
			require.NoError(t, err)
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

func TestAgentWakeAdmissionLegacyRunAndForeignRun(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name            string
		foreign, paused bool
		wantErr         error
	}{
		{name: "legacy run"},
		{
			name: "legacy under paused root", paused: true,
			wantErr: commerr.ErrInvalidState,
		},
		{name: "foreign run", foreign: true, wantErr: commerr.ErrNotFound},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, _, notice := agentWakeFixture(t)
			run := createAgentRunForTest(t.Context(), t, fixture.store,
				fixture.sessionID, fixture.turnID, 1)
			if tc.foreign {
				foreign := newTestSession(t.Context(), t, fixture.store)
				lease := startAgentRunTestTurn(t.Context(), t, fixture.store, foreign.ID)
				run = createAgentRunForTest(t.Context(), t, fixture.store,
					foreign.ID, lease.TurnID, 1)
				fixture.store.ReleaseTurn(lease)
			}
			if tc.paused {
				_, err := fixture.store.PauseExecution(t.Context(), fixture.sessionID,
					fixture.root.ID, executionTestPauseReason)
				require.NoError(t, err)
			}
			data, err := json.Marshal(events.AgentNoticeData{RunID: run.ID})
			require.NoError(t, err)
			notices := fixture.store.query.SessionNotice
			_, err = notices.WithContext(t.Context()).Where(notices.ID.Eq(notice.ID)).
				UpdateSimple(notices.DataJSON.Value(string(data)))
			require.NoError(t, err)
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

func TestAgentWakeAdmissionPauseSurvivesSQLiteReopen(t *testing.T) {
	t.Parallel()
	fixture, child, notice := agentWakeFixture(t)
	_, err := fixture.store.PauseExecution(t.Context(), fixture.sessionID,
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
	_, err = store.ResumeExecution(t.Context(), fixture.sessionID, child.ID)
	require.NoError(t, err)
	lease, err := store.AcquireTurn(t.Context(), fixture.sessionID, input)
	require.NoError(t, err)
	store.ReleaseTurn(lease)
}

func TestJobWakeAdmissionRefusesPausedRoot(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	job := createJobForTest(t.Context(), t, fixture.store,
		fixture.sessionID, fixture.turnID, 0)
	data, err := json.Marshal(map[string]uuid.UUID{"jobId": job.ID})
	require.NoError(t, err)
	notice, err := fixture.store.CreateSessionNotice(t.Context(), fixture.sessionID,
		CreateSessionNoticeInput{
			Type: events.TypeJobExited, Source: "tools.job",
			Summary: "job exited", DataJSON: string(data),
			Delivery: models.NoticeDeliveryWake,
		})
	require.NoError(t, err)
	fixture.store.ReleaseTurn(inboxTestLease(t, fixture))
	_, err = fixture.store.PauseExecution(t.Context(), fixture.sessionID,
		fixture.root.ID, executionTestPauseReason)
	require.NoError(t, err)
	_, err = fixture.store.AcquireTurn(t.Context(), fixture.sessionID,
		StartTurnInput{
			RequestID: uuid.New(), Workspace: testWorkspace,
			NoticeIDs: []uuid.UUID{notice.ID}, WakeNoticeID: &notice.ID,
		})
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	assert.False(t, fixture.store.IsActive(fixture.sessionID))
	pending, err := fixture.store.ListSessionNotices(t.Context(), fixture.sessionID,
		ListSessionNoticesOptions{State: models.NoticeStatePending})
	require.NoError(t, err)
	require.Len(t, pending.Items, 1)
	assert.Equal(t, notice.ID, pending.Items[0].ID)
}
