package cronstore

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConcurrentOccurrenceClaims(t *testing.T) {
	t.Parallel()
	store, _ := testStore(t)
	start := make(chan struct{})
	results := make(chan bool, 8)
	errors := make(chan error, 8)
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			<-start
			claimed, err := store.ClaimRun(t.Context(),
				testRun("nightlyReport", 10))
			results <- claimed
			errors <- err
		})
	}
	close(start)
	group.Wait()
	count := 0
	for range 8 {
		require.NoError(t, <-errors)
		if <-results {
			count++
		}
	}
	assert.Equal(t, 1, count)
	rows, more, err := store.ListRuns(t.Context(), "nightlyReport", 10, 0)
	require.NoError(t, err)
	assert.False(t, more)
	require.Len(t, rows, 1)
	again, err := store.ClaimRun(t.Context(), rows[0])
	require.NoError(t, err)
	assert.False(t, again)
}

func TestAttachAndFinishAreScopedToRunningRun(t *testing.T) {
	t.Parallel()
	store, handle := testStore(t)
	firstSession, otherSession := testSession(t, handle), testSession(t, handle)
	run := testRun("nightlyReport", 10)
	claimed, err := store.ClaimRun(t.Context(), run)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, store.AttachSession(t.Context(), run.ID, firstSession))
	require.NoError(t, store.AttachSession(t.Context(), run.ID, firstSession))
	require.ErrorIs(t, store.AttachSession(t.Context(), run.ID, otherSession),
		commerr.ErrConflict)
	finished := run.ScheduledAt.Add(time.Second)
	require.ErrorIs(t, store.FinishRun(t.Context(), run.ID,
		models.CronRunStateFailed, "private provider error", finished),
		commerr.ErrValidationFailed)
	require.ErrorIs(t, store.FinishRun(t.Context(), run.ID,
		models.CronRunStateFailed, ErrorRunFailed,
		run.ScheduledAt.Add(-time.Second)), commerr.ErrValidationFailed)
	require.NoError(t, store.FinishRun(t.Context(), run.ID,
		models.CronRunStateCompleted, "", finished))
	require.NoError(t, store.FinishRun(t.Context(), run.ID,
		models.CronRunStateFailed, ErrorTurnFailed, finished.Add(time.Second)))
	require.ErrorIs(t, store.AttachSession(t.Context(), run.ID, firstSession),
		commerr.ErrConflict)
	stored, err := store.LastRun(t.Context(), run.JobName)
	require.NoError(t, err)
	assert.Equal(t, models.CronRunStateCompleted, stored.State)
	assert.Empty(t, stored.Error)
	require.NotNil(t, stored.SessionID)
	assert.Equal(t, firstSession, *stored.SessionID)
	running, err := store.RunningRuns(t.Context())
	require.NoError(t, err)
	assert.Empty(t, running)
}

func TestRunValidation(t *testing.T) {
	t.Parallel()
	store, _ := testStore(t)
	testCases := []struct {
		name   string
		change func(*models.CronRun)
	}{
		{"nil ID", func(r *models.CronRun) { r.ID = uuid.Nil }},
		{"zero schedule", func(r *models.CronRun) {
			r.ScheduledAt = time.Time{}
		}},
		{"missing start", func(r *models.CronRun) { r.StartedAt = nil }},
		{"state", func(r *models.CronRun) { r.State = "unknown" }},
		{"raw error", func(r *models.CronRun) { r.Error = "private cause" }},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			run := testRun("nightlyReport", 10)
			tc.change(run)
			claimed, err := store.ClaimRun(t.Context(), run)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
			assert.False(t, claimed)
		})
	}
}

func TestMutationsDoNotChangeAnotherRun(t *testing.T) {
	t.Parallel()
	store, handle := testStore(t)
	selected := testRun("selectedJob", 10)
	untouched := testRun("otherJob", 10)
	for _, run := range []*models.CronRun{selected, untouched} {
		claimed, err := store.ClaimRun(t.Context(), run)
		require.NoError(t, err)
		require.True(t, claimed)
	}
	sessionID := testSession(t, handle)
	require.NoError(t, store.AttachSession(t.Context(), selected.ID, sessionID))
	require.NoError(t, store.FinishRun(t.Context(), selected.ID,
		models.CronRunStateCompleted, "", selected.ScheduledAt.Add(time.Second)))
	saved, err := store.LastRun(t.Context(), untouched.JobName)
	require.NoError(t, err)
	assert.Equal(t, models.CronRunStateRunning, saved.State)
	assert.Nil(t, saved.SessionID)
	assert.Nil(t, saved.FinishedAt)
	running, err := store.RunningRuns(t.Context())
	require.NoError(t, err)
	require.Len(t, running, 1)
	assert.Equal(t, untouched.ID, running[0].ID)
}
