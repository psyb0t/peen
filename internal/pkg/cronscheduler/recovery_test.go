package cronscheduler

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/cronstore"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSchedulerReconcilesUnadmittedAndTerminalRuns(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                   string
		attach, admit          bool
		state                  models.TurnState
		classification, reason string
	}{
		{name: "no session", reason: cronstore.ErrorInterrupted},
		{name: "no turn", attach: true, reason: cronstore.ErrorInterrupted},
		{name: "completed", attach: true, admit: true, state: models.TurnStateCompleted},
		{name: "timeout", attach: true, admit: true, state: models.TurnStateCancelled, classification: session.TurnFailureTimeout, reason: cronstore.ErrorTurnTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newSchedulerFixture(t)
			s, err := New(t.Context(), f.options(f.job()))
			require.NoError(t, err)
			t.Cleanup(s.Close)
			now := f.clock.Now()
			run := models.CronRun{ID: uuid.New(), JobName: f.job().Name, ScheduledAt: now, StartedAt: &now, State: models.CronRunStateRunning}
			if tc.attach {
				opened, openErr := f.sessions.Open(t.Context(), f.workspace, "control")
				require.NoError(t, openErr)
				run.SessionID = &opened.Session.ID
				if tc.admit {
					lease, leaseErr := f.turns.AcquireTurn(t.Context(), opened.Session.ID, session.StartTurnInput{RequestID: run.ID})
					require.NoError(t, leaseErr)
					require.NoError(t, f.turns.FinalizeTurn(t.Context(), lease, session.FinalizeTurnInput{State: tc.state, FailureClassification: tc.classification}))
					f.turns.ReleaseTurn(lease)
				}
			}
			claimed, err := f.store.ClaimRun(t.Context(), &run)
			require.NoError(t, err)
			require.True(t, claimed)
			require.NoError(t, s.recoverRuns(t.Context()))
			waitIdle(t, s)
			row, err := f.store.LastRun(t.Context(), run.JobName)
			require.NoError(t, err)
			assert.Equal(t, tc.reason, row.Error)
			assert.NotEqual(t, models.CronRunStateRunning, row.State)
			require.NotNil(t, row.FinishedAt)
		})
	}
}

func TestSchedulerWatchesAnAlreadyRunningTurn(t *testing.T) {
	t.Parallel()
	f := newSchedulerFixture(t)
	s, err := New(t.Context(), f.options(f.job()))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(func() { cancel(); s.Close() })
	opened, err := f.sessions.Open(t.Context(), f.workspace, "control")
	require.NoError(t, err)
	now := f.clock.Now()
	run := models.CronRun{ID: uuid.New(), JobName: f.job().Name, ScheduledAt: now, StartedAt: &now, SessionID: &opened.Session.ID, State: models.CronRunStateRunning}
	lease, err := f.turns.AcquireTurn(t.Context(), opened.Session.ID, session.StartTurnInput{RequestID: run.ID})
	require.NoError(t, err)
	t.Cleanup(func() { f.turns.ReleaseTurn(lease) })
	claimed, err := f.store.ClaimRun(t.Context(), &run)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, s.recoverRuns(ctx))
	select {
	case <-f.clock.armed:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery watcher did not arm")
	}
	f.clock.advance(time.Second)
	require.NoError(t, s.step(ctx))
	skipped, err := f.store.LastRun(t.Context(), run.JobName)
	require.NoError(t, err)
	assert.Equal(t, cronstore.ErrorPreviousRunning, skipped.Error)
	require.NoError(t, f.turns.FinalizeTurn(t.Context(), lease, session.FinalizeTurnInput{State: models.TurnStateCompleted}))
	f.clock.wake <- f.clock.Now()
	waitIdle(t, s)
	rows, _, err := f.store.ListRuns(t.Context(), run.JobName, 200, 0)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, models.CronRunStateCompleted, rows[1].State)
	assert.Empty(t, f.seen)
}
