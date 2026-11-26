package cronstore

import (
	"strings"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJobScheduleBaselineSurvivesReopen(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	handle, err := db.Open(t.Context(), db.Config{Directory: directory})
	require.NoError(t, err)
	store, err := New(handle)
	require.NoError(t, err)
	job := testJob("nightlyReport")
	require.NoError(t, store.SaveJob(t.Context(), job))
	require.NoError(t, handle.Close())
	reopened, err := db.Open(t.Context(), db.Config{Directory: directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	resumed, err := New(reopened)
	require.NoError(t, err)
	stored, err := resumed.GetJob(t.Context(), job.JobName)
	require.NoError(t, err)
	assert.True(t, job.NextScheduledAt.Equal(stored.NextScheduledAt))
	stored.DefinitionHash = strings.Repeat("b", 64)
	stored.NextScheduledAt = stored.NextScheduledAt.Add(time.Hour)
	require.NoError(t, resumed.SaveJob(t.Context(), stored))
	reset, err := resumed.GetJob(t.Context(), job.JobName)
	require.NoError(t, err)
	assert.Equal(t, stored.DefinitionHash, reset.DefinitionHash)
	assert.True(t, stored.NextScheduledAt.Equal(reset.NextScheduledAt))
}

func TestMissingAndInvalidInputs(t *testing.T) {
	t.Parallel()
	store, _ := testStore(t)
	_, err := New(nil)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	_, err = store.GetJob(t.Context(), "missing")
	require.ErrorIs(t, err, commerr.ErrNotFound)
	_, err = store.LastRun(t.Context(), "missing")
	require.ErrorIs(t, err, commerr.ErrNotFound)
	for _, name := range []string{
		"", "../escape", "line\nfeed",
		strings.Repeat("a", 65),
	} {
		require.ErrorIs(t, store.SaveJob(t.Context(), testJob(name)),
			commerr.ErrValidationFailed)
	}
	job := testJob("test")
	job.DefinitionHash = strings.Repeat("z", 64)
	require.ErrorIs(t, store.SaveJob(t.Context(), job),
		commerr.ErrValidationFailed)
}

func TestNullableSessionsAndSkippedRun(t *testing.T) {
	t.Parallel()
	store, _ := testStore(t)
	job := testJob("nightlyReport")
	require.NoError(t, store.SaveJob(t.Context(), job))
	run := testRun(job.JobName, 10)
	run.State = models.CronRunStateSkipped
	run.StartedAt = nil
	finished := run.ScheduledAt
	run.FinishedAt = &finished
	run.Error = ErrorControllerOffline
	claimed, err := store.ClaimRun(t.Context(), run)
	require.NoError(t, err)
	require.True(t, claimed)
	saved, err := store.LastRun(t.Context(), job.JobName)
	require.NoError(t, err)
	assert.Nil(t, saved.StartedAt)
	assert.Nil(t, saved.SessionID)
	assert.Equal(t, models.CronRunStateSkipped, saved.State)
}

func TestStoredTimesAreUTC(t *testing.T) {
	t.Parallel()
	store, _ := testStore(t)
	zone := time.FixedZone("fixture", 2*60*60)
	job := testJob("nightlyReport")
	job.NextScheduledAt = job.NextScheduledAt.In(zone)
	require.NoError(t, store.SaveJob(t.Context(), job))
	loaded, err := store.GetJob(t.Context(), job.JobName)
	require.NoError(t, err)
	_, offset := loaded.NextScheduledAt.Zone()
	assert.Zero(t, offset)
	assert.True(t, job.NextScheduledAt.Equal(loaded.NextScheduledAt))
	run := testRun(job.JobName, 10)
	run.ScheduledAt = run.ScheduledAt.In(zone)
	started := run.StartedAt.In(zone)
	run.StartedAt = &started
	claimed, err := store.ClaimRun(t.Context(), run)
	require.NoError(t, err)
	require.True(t, claimed)
	running, err := store.RunningRuns(t.Context())
	require.NoError(t, err)
	require.Len(t, running, 1)
	_, offset = running[0].ScheduledAt.Zone()
	assert.Zero(t, offset)
	require.NotNil(t, running[0].StartedAt)
	_, offset = running[0].StartedAt.Zone()
	assert.Zero(t, offset)
}
