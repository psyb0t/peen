package cronscheduler

import (
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/cronconfig"
	"github.com/psyb0t/peen/internal/pkg/cronstore"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSchedulerRefusesAJobOutsideItsWorkspaceAgentSpace(t *testing.T) {
	t.Parallel()
	f := newSchedulerFixture(t)
	_, err := f.sessions.Open(t.Context(), f.workspace, sandboxSpace)
	require.NoError(t, err)
	job := f.job()
	canonical, err := f.sessions.ResolveWorkspace(f.workspace)
	require.NoError(t, err)
	_, err = New(t.Context(), f.options(job))
	require.ErrorIs(t, err, commerr.ErrConflict)
	assert.Contains(t, err.Error(), job.Name)
	assert.Contains(t, err.Error(), `agentSpace "control"`)
	assert.Contains(t, err.Error(), `agent space "docker:sandbox"`)
	assert.Contains(t, err.Error(), canonical)
}

func TestSchedulerRefusesJobsNamingDifferentAgentSpacesForOneWorkspace(
	t *testing.T,
) {
	t.Parallel()
	f := newSchedulerFixture(t)
	first := f.job()
	second := f.job()
	second.Name = "weeklyReport"
	second.AgentSpace = sandboxSpace
	options := f.options(first)
	options.Jobs = []cronconfig.Job{first, second}
	_, err := New(t.Context(), options)
	require.ErrorIs(t, err, commerr.ErrConflict)
	assert.Contains(t, err.Error(), first.Name)
	assert.Contains(t, err.Error(), second.Name)
}

func TestSchedulerJobWithoutAgentSpaceFollowsItsWorkspace(t *testing.T) {
	t.Parallel()
	f := newSchedulerFixture(t)
	_, err := f.sessions.Open(t.Context(), f.workspace, sandboxSpace)
	require.NoError(t, err)
	job := f.job()
	job.AgentSpace = ""
	s, err := New(t.Context(), f.options(job))
	require.NoError(t, err)
	t.Cleanup(s.Close)
	for range 2 {
		f.clock.advance(time.Second)
		require.NoError(t, s.step(t.Context()))
		waitIdle(t, s)
		input := nextInput(t, f)
		stored, getErr := f.turns.Get(t.Context(), input.SessionID)
		require.NoError(t, getErr)
		assert.Equal(t, sandboxSpace, stored.AgentSpace)
	}
	row, err := f.store.LastRun(t.Context(), job.Name)
	require.NoError(t, err)
	assert.Equal(t, models.CronRunStateCompleted, row.State)
}

func TestSchedulerRecordsAFireTimeAgentSpaceConflict(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{
		cronconfig.SessionNew, cronconfig.SessionContinue,
	} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newSchedulerFixture(t)
			job := f.job()
			job.Session = mode
			s, err := New(t.Context(), f.options(job))
			require.NoError(t, err)
			t.Cleanup(s.Close)
			f.clock.advance(time.Second)
			require.NoError(t, s.step(t.Context()))
			waitIdle(t, s)
			first := nextInput(t, f)
			_, err = f.sessions.ReconfigureSession(t.Context(),
				first.SessionID, api.ReconfigureSessionRequest{
					AgentSpace: sandboxSpace, Reason: "isolate the workspace",
				})
			require.NoError(t, err)
			f.clock.advance(time.Second)
			require.NoError(t, s.step(t.Context()))
			waitIdle(t, s)
			assert.Empty(t, f.seen)
			row, err := f.store.LastRun(t.Context(), job.Name)
			require.NoError(t, err)
			assert.Equal(t, models.CronRunStateFailed, row.State)
			assert.Equal(t, cronstore.ErrorAgentSpaceConflict, row.Error)
		})
	}
}
