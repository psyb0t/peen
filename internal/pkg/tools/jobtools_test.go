package tools

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	jobToolsPurpose         = "job tools test"
	jobToolsEventuallyWait  = 5 * time.Second
	jobToolsEventuallyTick  = 5 * time.Millisecond
	jobToolsMaxOutputLines  = 3
	jobToolsMaxOutputBytes  = 1024
	jobToolsReadWindowLines = 2
	jobToolsLineCount       = 20
)

func TestListJobs_ReportsThisSessionsJobsOldestFirst(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)

	first, err := executor.RunCommand(context.Background(), RunCommandInput{
		Purpose: jobToolsPurpose,
		Command: "true",
	})
	require.NoError(t, err)

	second, err := executor.RunCommand(context.Background(), RunCommandInput{
		Purpose: jobToolsPurpose,
		Command: "true",
	})
	require.NoError(t, err)

	out, err := executor.ListJobs(context.Background(), ListJobsInput{})
	require.NoError(t, err)

	require.Len(t, out.Jobs, 2)
	assert.False(t, out.Truncated)
	assert.Equal(t, first.JobID, out.Jobs[0].JobID)
	assert.Equal(t, second.JobID, out.Jobs[1].JobID)
	assert.Equal(t, jobToolsPurpose, out.Jobs[0].Purpose)
}

func TestListJobs_FiltersByState(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)

	_, err := executor.RunCommand(context.Background(), RunCommandInput{
		Purpose: jobToolsPurpose,
		Command: "true",
	})
	require.NoError(t, err)

	running, err := executor.RunCommand(context.Background(), RunCommandInput{
		Purpose:    jobToolsPurpose,
		Command:    "sleep 30",
		Background: true,
	})
	require.NoError(t, err)

	out, err := executor.ListJobs(context.Background(), ListJobsInput{
		State: JobStateRunning,
	})
	require.NoError(t, err)

	require.Len(t, out.Jobs, 1)
	assert.Equal(t, running.JobID, out.Jobs[0].JobID)

	_, ok := executor.jobs.Signal(context.Background(), running.JobID, JobSignalKill)
	require.True(t, ok)
}

func TestListJobs_TruncatesOverBound(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutorWithLimits(t, Limits{MaxListJobs: 1})

	_, err := executor.RunCommand(context.Background(), RunCommandInput{
		Purpose: jobToolsPurpose,
		Command: "true",
	})
	require.NoError(t, err)

	_, err = executor.RunCommand(context.Background(), RunCommandInput{
		Purpose: jobToolsPurpose,
		Command: "true",
	})
	require.NoError(t, err)

	out, err := executor.ListJobs(context.Background(), ListJobsInput{})
	require.NoError(t, err)

	assert.Len(t, out.Jobs, 1)
	assert.True(t, out.Truncated)
}

func TestReadJobOutput_UnknownJobNotFound(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)

	out, err := executor.ReadJobOutput(context.Background(), ReadJobOutputInput{
		JobID: uuid.New(),
	})
	require.NoError(t, err)

	assert.False(t, out.Found)
}

func TestReadJobOutput_InvalidStream(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)

	out, err := executor.RunCommand(context.Background(), RunCommandInput{
		Purpose: jobToolsPurpose,
		Command: "true",
	})
	require.NoError(t, err)

	_, err = executor.ReadJobOutput(context.Background(), ReadJobOutputInput{
		JobID:  out.JobID,
		Stream: "nonsense",
	})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

// TestReadJobOutput_IncrementalCursor proves a cursor lets a caller catch up
// on a job's output across more than one call, and that the cursor reported
// after the buffer overflows accounts for the lines dropped before it.
func TestReadJobOutput_IncrementalCursor(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutorWithLimits(t, Limits{
		MaxJobOutputLines: jobToolsMaxOutputLines,
	})

	out, err := executor.RunCommand(context.Background(), RunCommandInput{
		Purpose:    jobToolsPurpose,
		Background: true,
		Command: fmt.Sprintf(
			"i=0; while [ $i -lt %d ]; do echo line$i; i=$((i+1)); "+
				"sleep 0.02; done",
			jobToolsLineCount,
		),
	})
	require.NoError(t, err)

	job, ok := executor.jobs.Get(out.JobID)
	require.True(t, ok)

	// First window: read whatever has accumulated so far with a bounded
	// window size, then keep following the cursor forward.
	first, err := executor.ReadJobOutput(context.Background(), ReadJobOutputInput{
		JobID:    out.JobID,
		Stream:   JobStreamStdout,
		MaxLines: jobToolsReadWindowLines,
	})
	require.NoError(t, err)
	assert.True(t, first.Found)

	cursor := first.NextStdoutCursor

	require.Eventually(t, func() bool {
		return job.Snapshot().State != JobStateRunning
	}, jobToolsEventuallyWait, jobToolsEventuallyTick, "job never exited")

	final, err := executor.ReadJobOutput(context.Background(), ReadJobOutputInput{
		JobID:        out.JobID,
		Stream:       JobStreamStdout,
		StdoutCursor: cursor,
	})
	require.NoError(t, err)

	assert.True(t, final.Found)
	assert.Positive(t, final.StdoutDroppedLines)
	assert.NotEmpty(t, final.Stdout)
}

func TestWaitJob_UnknownJobNotFound(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)

	out, err := executor.WaitJob(context.Background(), WaitJobInput{
		JobID: uuid.New(),
	})
	require.NoError(t, err)

	assert.False(t, out.Found)
}

// TestWaitJob_InvalidTimeoutBeatsUnknownJob pins that the timeout is
// validated before the job lookup, so a bad timeout is never accepted just
// because the job ID names nothing.
func TestWaitJob_InvalidTimeoutBeatsUnknownJob(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)

	_, err := executor.WaitJob(context.Background(), WaitJobInput{
		JobID:   uuid.New(),
		Timeout: "soon",
	})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	assert.Contains(
		t,
		err.Error(),
		`timeout "soon" must be a Go duration such as 90s, 5m, or 1h30m`,
	)
}

func TestWaitJob_ReturnsOnceJobExits(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)

	started, err := executor.RunCommand(context.Background(), RunCommandInput{
		Purpose:    jobToolsPurpose,
		Command:    "sleep 0.05; exit 3",
		Background: true,
	})
	require.NoError(t, err)

	out, err := executor.WaitJob(context.Background(), WaitJobInput{
		JobID: started.JobID,
	})
	require.NoError(t, err)

	assert.True(t, out.Found)
	assert.False(t, out.Running)
	assert.Equal(t, JobStateExited, out.State)
	assert.Equal(t, 3, out.ExitCode)
	assert.GreaterOrEqual(t, out.DurationMs, int64(0))
	assert.NotEmpty(t, out.Duration)
}

// TestJobWaitOutput_DurationIsAGoDuration pins the duration text for a job
// with fixed start and end times.
func TestJobWaitOutput_DurationIsAGoDuration(t *testing.T) {
	t.Parallel()

	started := time.Date(2026, time.October, 10, 9, 0, 0, 0, time.UTC)
	job := &Job{
		ID:        uuid.New(),
		StartedAt: started,
		stdout:    newJobBuffer(jobToolsMaxOutputLines, jobToolsMaxOutputBytes),
		stderr:    newJobBuffer(jobToolsMaxOutputLines, jobToolsMaxOutputBytes),
		state:     JobStateExited,
		endedAt:   started.Add(90 * time.Second),
	}

	out := jobWaitOutput(job, WaitJobInput{})

	assert.Equal(t, int64(90000), out.DurationMs)
	assert.Equal(t, "1m30s", out.Duration)
}

func TestWaitJob_BoundExpiresJobStillRunning(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)

	started, err := executor.RunCommand(context.Background(), RunCommandInput{
		Purpose:    jobToolsPurpose,
		Command:    "sleep 30",
		Background: true,
	})
	require.NoError(t, err)

	out, err := executor.WaitJob(context.Background(), WaitJobInput{
		JobID:   started.JobID,
		Timeout: "1s",
	})
	require.NoError(t, err)

	assert.True(t, out.Found)
	assert.True(t, out.Running)
	assert.Equal(t, JobStateRunning, out.State)

	_, ok := executor.jobs.Signal(context.Background(), started.JobID, JobSignalKill)
	require.True(t, ok)
}

func TestWaitJob_RejectsInvalidTimeout(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)

	started, err := executor.RunCommand(context.Background(), RunCommandInput{
		Purpose: jobToolsPurpose,
		Command: "true",
	})
	require.NoError(t, err)

	testCases := []struct {
		timeout string
		message string
	}{
		{timeout: "-1s", message: `timeout "-1s" must be positive`},
		{timeout: "0s", message: `timeout "0s" must be positive`},
		{
			timeout: "10",
			message: `timeout "10" must be a Go duration such as 90s, 5m, or 1h30m`,
		},
		{
			timeout: "soon",
			message: `timeout "soon" must be a Go duration such as 90s, 5m, or 1h30m`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.timeout, func(t *testing.T) {
			t.Parallel()

			_, err := executor.WaitJob(context.Background(), WaitJobInput{
				JobID:   started.JobID,
				Timeout: tc.timeout,
			})
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
			assert.Contains(t, err.Error(), tc.message)
		})
	}
}

func TestResolveWaitJobTimeout(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)
	limits := executor.Limits()

	testCases := []struct {
		name    string
		timeout string
		want    time.Duration
	}{
		{name: "empty uses the default", timeout: "", want: limits.WaitJobTimeout},
		{name: "minutes and seconds", timeout: "1m30s", want: 90 * time.Second},
		{name: "above the maximum is clamped", timeout: "999h", want: limits.MaxWaitJobTimeout},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := executor.resolveWaitJobTimeout(tc.timeout)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSignalJob_UnknownJobIdempotent(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)

	out, err := executor.SignalJob(context.Background(), SignalJobInput{
		JobID:  uuid.New(),
		Signal: JobSignalStop,
	})
	require.NoError(t, err)

	assert.False(t, out.Found)
}

func TestSignalJob_InvalidSignal(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)

	started, err := executor.RunCommand(context.Background(), RunCommandInput{
		Purpose: jobToolsPurpose,
		Command: "true",
	})
	require.NoError(t, err)

	_, err = executor.SignalJob(context.Background(), SignalJobInput{
		JobID:  started.JobID,
		Signal: "nonsense",
	})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

func TestSignalJob_KillsRunningJob(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)

	started, err := executor.RunCommand(context.Background(), RunCommandInput{
		Purpose:    jobToolsPurpose,
		Command:    "sleep 30",
		Background: true,
	})
	require.NoError(t, err)

	out, err := executor.SignalJob(context.Background(), SignalJobInput{
		JobID:  started.JobID,
		Signal: JobSignalKill,
	})
	require.NoError(t, err)

	assert.True(t, out.Found)
	assert.Equal(t, JobStateRunning, out.State)

	assert.Eventually(t, func() bool {
		job, ok := executor.jobs.Get(started.JobID)

		return ok && job.Snapshot().State == JobStateSignalled
	}, jobToolsEventuallyWait, jobToolsEventuallyTick,
		"job must finalize as signalled")
}
