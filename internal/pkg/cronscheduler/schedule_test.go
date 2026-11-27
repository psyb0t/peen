package cronscheduler

import (
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/cronconfig"
	"github.com/psyb0t/peen/internal/pkg/cronstore"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLatestOccurrenceCalendarAndInterval(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		schedule         string
		first, now, want string
	}{
		{schedule: "0 9 * * *", first: "2026-03-01T09:00:00Z", now: "2026-03-31T08:59:59Z", want: "2026-03-30T09:00:00Z"},
		{schedule: "@every 7s", first: "2026-03-01T09:00:00Z", now: "2026-03-01T09:00:20Z", want: "2026-03-01T09:00:14Z"},
		{schedule: "0 0 * * *", first: "2026-03-08T05:00:00Z", now: "2026-03-10T03:59:59Z", want: "2026-03-09T04:00:00Z"},
	} {
		t.Run(tc.schedule, func(t *testing.T) {
			t.Parallel()
			data := "jobs:\n  - name: report\n    schedule: '" + tc.schedule + "'\n    timeZone: America/New_York\n    workspace: /srv/work\n    message: run\n"
			if tc.schedule == "0 9 * * *" {
				data = "jobs:\n  - name: report\n    schedule: '" + tc.schedule + "'\n    workspace: /srv/work\n    message: run\n"
			}
			jobs, err := cronconfig.Parse([]byte(data), cronconfig.Options{})
			require.NoError(t, err)
			first, err := time.Parse(time.RFC3339, tc.first)
			require.NoError(t, err)
			now, err := time.Parse(time.RFC3339, tc.now)
			require.NoError(t, err)
			want, err := time.Parse(time.RFC3339, tc.want)
			require.NoError(t, err)
			assert.Equal(t, want, latestOccurrence(jobs[0].Schedule, first, now))
			assert.True(t, latestOccurrence(jobs[0].Schedule, first, first.Add(-time.Second)).IsZero())
		})
	}
}

func TestSchedulerRefusesOutsidePolicyAndReplayedTick(t *testing.T) {
	t.Parallel()
	f := newSchedulerFixture(t)
	job := f.job()
	job.Workspace = t.TempDir()
	_, err := New(t.Context(), f.options(job))
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	job = f.job()
	s, err := New(t.Context(), f.options(job))
	require.NoError(t, err)
	t.Cleanup(s.Close)
	due := s.jobs[0].record.NextScheduledAt
	f.clock.advance(time.Second)
	require.NoError(t, s.step(t.Context()))
	waitIdle(t, s)
	s.mutex.Lock()
	s.jobs[0].record.NextScheduledAt = due
	s.mutex.Unlock()
	require.NoError(t, s.step(t.Context()))
	waitIdle(t, s)
	rows, _, err := f.store.ListRuns(t.Context(), "", 200, 0)
	require.NoError(t, err)
	assert.Len(t, rows, 1)
	assert.Len(t, f.seen, 1)
}

func TestSchedulerBusyAndExpandedMessageLimit(t *testing.T) {
	t.Parallel()
	// A session worker reports a busy session as ErrConflict, and an
	// in-process runtime as ErrLockHeld. Both skip the tick.
	for _, tc := range []struct {
		name    string
		busyErr error
	}{
		{name: "busy in process", busyErr: commerr.ErrLockHeld},
		{name: "busy session worker", busyErr: ctxerrors.Wrap(
			commerr.ErrConflict, "session already has an active turn")},
		{name: "expanded limit"},
	} {
		busy := tc.busyErr != nil
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newSchedulerFixture(t)
			options := f.options(f.job())
			if busy {
				f.err = tc.busyErr
			} else {
				options.MaxMessageBytes = 1
			}
			s, err := New(t.Context(), options)
			require.NoError(t, err)
			t.Cleanup(s.Close)
			f.clock.advance(time.Second)
			require.NoError(t, s.step(t.Context()))
			waitIdle(t, s)
			row, err := f.store.LastRun(t.Context(), f.job().Name)
			require.NoError(t, err)
			if busy {
				assert.Equal(t, models.CronRunStateSkipped, row.State)
				assert.Equal(t, cronstore.ErrorWorkspaceBusy, row.Error)
			} else {
				assert.Equal(t, models.CronRunStateFailed, row.State)
				assert.Empty(t, f.seen)
			}
		})
	}
}
