package cronscheduler

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/cronstore"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSchedulerContinuationAndSettings(t *testing.T) {
	t.Parallel()
	f := newSchedulerFixture(t)
	job := f.job()
	job.Model = "example/model"
	job.Timeout = 30 * time.Second
	job.OutputSchema = []byte(`{"type":"object"}`)
	s, err := New(t.Context(), f.options(job))
	require.NoError(t, err)
	t.Cleanup(s.Close)
	canonical, err := f.sessions.Open(t.Context(), f.workspace, "control")
	require.NoError(t, err)
	var id uuid.UUID
	for range 2 {
		f.clock.advance(time.Second)
		require.NoError(t, s.step(t.Context()))
		waitIdle(t, s)
		input := nextInput(t, f)
		if id == uuid.Nil {
			id = input.SessionID
		}
		assert.Equal(t, id, input.SessionID)
		assert.Equal(t, canonical.Session.ID, id)
		assert.NotEqual(t, uuid.Nil, input.RequestID)
		assert.Contains(t, input.Message.Message, job.Name)
		assert.NotContains(t, input.Message.Message, "{scheduledAt}")
		assert.Equal(t, 30, input.Message.TimeoutSeconds)
		assert.JSONEq(t, string(job.OutputSchema), string(input.Message.OutputSchema))
	}
	page, err := s.ListCronRuns(t.Context(), api.ListCronRunsParams{})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.EqualValues(t, models.CronRunStateCompleted, page.Items[0].State)
	assert.Equal(t, id, *page.Items[0].SessionId)
	jobs, err := s.ListCronJobs(t.Context())
	require.NoError(t, err)
	require.Len(t, jobs.Items, 1)
	require.NotNil(t, jobs.Items[0].LastRun)
	assert.True(t, jobs.Items[0].NextScheduledAt.After(f.clock.Now()))
}

func TestSchedulerOverlapAndLatestDowntimeTick(t *testing.T) {
	t.Parallel()
	f := newSchedulerFixture(t)
	release := make(chan struct{})
	f.release = release
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s, err := New(ctx, f.options(f.job()))
	require.NoError(t, err)
	t.Cleanup(func() { cancel(); s.Close() })
	f.clock.advance(time.Second)
	require.NoError(t, s.step(ctx))
	require.Eventually(t, func() bool { return len(f.seen) == 1 }, 5*time.Second, time.Millisecond)
	f.clock.advance(time.Second)
	require.NoError(t, s.step(ctx))
	last, err := f.store.LastRun(t.Context(), f.job().Name)
	require.NoError(t, err)
	assert.Equal(t, models.CronRunStateSkipped, last.State)
	assert.Equal(t, cronstore.ErrorPreviousRunning, last.Error)
	close(release)
	waitIdle(t, s)
	s.Close()
	f.clock.advance(100 * time.Second)
	s, err = New(t.Context(), f.options(f.job()))
	require.NoError(t, err)
	last, err = f.store.LastRun(t.Context(), f.job().Name)
	require.NoError(t, err)
	assert.Equal(t, cronstore.ErrorControllerOffline, last.Error)
	assert.True(t, last.ScheduledAt.Equal(f.clock.Now()))
	rows, _, err := f.store.ListRuns(t.Context(), "", 200, 0)
	require.NoError(t, err)
	assert.Len(t, rows, 3)
	assert.Len(t, f.seen, 1)
	s.Close()
}

func TestSchedulerShutdownAndInterruptedTurnRecovery(t *testing.T) {
	t.Parallel()
	f := newSchedulerFixture(t)
	f.release = make(chan struct{})
	s, err := New(t.Context(), f.options(f.job()))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.clock.advance(time.Second)
	require.NoError(t, s.step(ctx))
	require.Eventually(t, func() bool { return len(f.seen) == 1 }, 5*time.Second, time.Millisecond)
	input := nextInput(t, f)
	require.Eventually(t, func() bool { return f.turns.IsActive(input.SessionID) }, 5*time.Second, time.Millisecond)
	cancel()
	waitIdle(t, s)
	s.Close()
	row, err := f.store.LastRun(t.Context(), f.job().Name)
	require.NoError(t, err)
	assert.Equal(t, models.CronRunStateRunning, row.State)
	_, err = f.turns.RecoverInterrupted(t.Context())
	require.NoError(t, err)
	s, err = New(t.Context(), f.options(f.job()))
	require.NoError(t, err)
	t.Cleanup(s.Close)
	require.NoError(t, s.recoverRuns(t.Context()))
	row, err = f.store.LastRun(t.Context(), f.job().Name)
	require.NoError(t, err)
	assert.Equal(t, models.CronRunStateFailed, row.State)
	assert.Equal(t, cronstore.ErrorInterrupted, row.Error)
}

func TestSchedulerSafeOutcomes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		state       models.TurnState
		reason      string
		rawError    bool
		outputError string
	}{
		{name: "cancelled", state: models.TurnStateCancelled, reason: cronstore.ErrorTurnCancelled},
		{name: "failed", state: models.TurnStateFailed, reason: cronstore.ErrorTurnFailed},
		{name: "runner error", reason: cronstore.ErrorRunFailed, rawError: true},
		{name: "schema", state: models.TurnStateCompleted, reason: cronstore.ErrorOutputInvalid, outputError: "private schema details"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newSchedulerFixture(t)
			f.state = tc.state
			f.outputError = tc.outputError
			if tc.rawError {
				f.err = commerr.ErrExecFailed
			}
			s, err := New(t.Context(), f.options(f.job()))
			require.NoError(t, err)
			t.Cleanup(s.Close)
			f.clock.advance(time.Second)
			require.NoError(t, s.step(t.Context()))
			waitIdle(t, s)
			row, err := f.store.LastRun(t.Context(), f.job().Name)
			require.NoError(t, err)
			assert.Equal(t, tc.reason, row.Error)
			assert.NotContains(t, row.Error, "private")
		})
	}
}

func TestSchedulerClockLoopAndClose(t *testing.T) {
	t.Parallel()
	f := newSchedulerFixture(t)
	s, err := New(t.Context(), f.options(f.job()))
	require.NoError(t, err)
	t.Cleanup(s.Close)
	done := make(chan error, 1)
	go func() { done <- s.Run(t.Context()) }()
	select {
	case <-f.clock.armed:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not arm")
	}
	f.clock.advance(time.Second)
	f.clock.wake <- f.clock.Now()
	require.Eventually(t, func() bool { return len(f.seen) == 1 }, 5*time.Second, time.Millisecond)
	s.Close()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not stop")
	}
	require.ErrorIs(t, s.Run(t.Context()), commerr.ErrInvalidState)
	f.clock.advance(time.Second)
	require.NoError(t, s.step(t.Context()))
	assert.Len(t, f.seen, 1)
	_, err = New(t.Context(), Options{})
	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
}
