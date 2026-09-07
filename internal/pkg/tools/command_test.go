package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	commandExitCodeNonZero   = 7
	commandTestPurpose       = "test command"
	commandWaitSeconds       = 1
	commandWaitSlack         = 4 * time.Second
	commandOverflowBytes     = 100000
	commandEventuallyWait    = 5 * time.Second
	commandEventuallyTick    = 5 * time.Millisecond
	commandTinyOutputLines   = 4
	commandOverflowLineCount = 40
	commandBackgroundSeconds = 30

	// commandRaceRetries and commandRaceBackoff bound and pace retries
	// working around a confirmed upstream race in commander v0.5.8, not
	// this package's own logic: see runCommandRetryingRace. Heavy
	// concurrent forking (the full package test run, not just this file)
	// can push the underlying race's loss rate close to total for a short
	// window, so this needs real headroom, not just a couple of attempts.
	commandRaceRetries = 40
	commandRaceBackoff = 20 * time.Millisecond
)

func newTestExecutor(t *testing.T) *Executor {
	t.Helper()

	executor, err := NewExecutor(Options{Workspace: t.TempDir()})
	require.NoError(t, err)

	return executor
}

func newTestExecutorWithLimits(t *testing.T, limits Limits) *Executor {
	t.Helper()

	executor, err := NewExecutor(Options{
		Workspace: t.TempDir(),
		Limits:    limits,
	})
	require.NoError(t, err)

	return executor
}

// newTestJobExecutor builds a JobExecutor over a fresh executor and a fresh,
// dedicated job registry so tests never share job state.
func newTestJobExecutor(t *testing.T) *JobExecutor {
	t.Helper()

	return newTestJobExecutorWithLimits(t, Limits{})
}

func newTestJobExecutorWithLimits(t *testing.T, limits Limits) *JobExecutor {
	t.Helper()

	executor := newTestExecutorWithLimits(t, limits)

	registry, err := NewJobRegistry(uuid.New(), nil, limits)
	require.NoError(t, err)

	jobExecutor, err := NewJobExecutor(executor, registry, uuid.New())
	require.NoError(t, err)

	return jobExecutor
}

// processAlive reports a genuinely running process, not merely an
// existing PID slot. kill(pid, 0) alone cannot tell a running process from
// a zombie: a killed grandchild's direct parent (the shell) is reaped by
// this package's own cmd.Wait, but nothing in this process tree waits on
// the grandchild, so it lingers as a zombie until some ancestor reaps it.
// kill(pid, 0) reports a zombie's PID as present, so tests read /proc
// instead and treat state 'Z' as dead.
func processAlive(t *testing.T, pid int) bool {
	t.Helper()

	if pid <= 0 {
		return false
	}

	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}

	closeParen := bytes.LastIndexByte(data, ')')
	if closeParen < 0 || closeParen+2 >= len(data) {
		return false
	}

	state := data[closeParen+2]

	return state != 'Z'
}

// runCommandRetryingRace works around a confirmed upstream race in
// commander v0.5.8 (not this package's code): commander's internal
// discardInternalOutput goroutine selects between its own shutdown signal
// and the last still-pending buffered output line(s) at process exit, and
// Go's select picks among simultaneously ready cases at random, so a fast
// command's captured output is lost non-deterministically, independent of
// scheduling delay before the command even runs (verified with a minimal
// reproduction using only commander, isolated from this package: see the
// implementation report). raced reports whether a result looks like this
// known symptom; the call is retried, bounded, until it does not.
func runCommandRetryingRace(
	t *testing.T,
	executor *JobExecutor,
	input RunCommandInput,
	raced func(RunCommandOutput) bool,
) RunCommandOutput {
	t.Helper()

	var out RunCommandOutput

	for attempt := range commandRaceRetries {
		if attempt > 0 {
			time.Sleep(commandRaceBackoff)
		}

		result, err := executor.RunCommand(context.Background(), input)
		require.NoError(t, err)

		out = result
		if !raced(result) {
			return out
		}
	}

	return out
}

func TestRunCommand_ValidationErrors(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		input RunCommandInput
	}{
		{
			name:  "empty command",
			input: RunCommandInput{Purpose: commandTestPurpose, Command: ""},
		},
		{
			name: "whitespace only command",
			input: RunCommandInput{
				Purpose: commandTestPurpose,
				Command: "   ",
			},
		},
		{
			name:  "missing purpose",
			input: RunCommandInput{Command: "true"},
		},
		{
			name: "negative timeout",
			input: RunCommandInput{
				Purpose:        commandTestPurpose,
				Command:        "true",
				TimeoutSeconds: -1,
			},
		},
		{
			name: "empty environment key",
			input: RunCommandInput{
				Purpose:     commandTestPurpose,
				Command:     "true",
				Environment: map[string]string{"": "x"},
			},
		},
		{
			name: "environment key with equals",
			input: RunCommandInput{
				Purpose:     commandTestPurpose,
				Command:     "true",
				Environment: map[string]string{"FOO=BAR": "x"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			executor := newTestJobExecutor(t)

			_, err := executor.RunCommand(context.Background(), tc.input)

			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}

func TestRunCommand_MissingDirectory(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)
	missing := filepath.Join(executor.Workspace(), "does-not-exist")

	_, err := executor.RunCommand(context.Background(), RunCommandInput{
		Purpose:   commandTestPurpose,
		Command:   "true",
		Directory: missing,
	})

	require.ErrorIs(t, err, commerr.ErrNotFound)
}

func TestRunCommand_ExitsInBound_ReturnsStatusNoJobLeftRunning(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)

	input := RunCommandInput{
		Purpose: commandTestPurpose,
		Command: "printf 'hello'",
	}
	out := runCommandRetryingRace(t, executor, input, func(o RunCommandOutput) bool {
		return o.Stdout == ""
	})

	assert.Equal(t, JobStateExited, out.State)
	assert.False(t, out.Running)
	assert.Equal(t, 0, out.ExitCode)
	assert.Equal(t, "hello", out.Stdout)
	assert.Empty(t, out.Stderr)
	assert.Equal(t, executor.Workspace(), out.Directory)
	assert.Equal(t, commandTestPurpose, out.Purpose)
	assert.NotEqual(t, uuid.Nil, out.JobID)

	assert.Eventually(t, func() bool {
		return !processAlive(t, out.PID)
	}, commandEventuallyWait, commandEventuallyTick,
		"process must not still be running once exited")
}

func TestRunCommand_StderrCapturedSeparately(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)

	input := RunCommandInput{
		Purpose: commandTestPurpose,
		Command: "printf 'oops' 1>&2",
	}
	out := runCommandRetryingRace(t, executor, input, func(o RunCommandOutput) bool {
		return o.Stderr == ""
	})

	assert.Equal(t, 0, out.ExitCode)
	assert.Empty(t, out.Stdout)
	assert.Equal(t, "oops", out.Stderr)
}

func TestRunCommand_NonZeroExit(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)

	out, err := executor.RunCommand(context.Background(), RunCommandInput{
		Purpose: commandTestPurpose,
		Command: fmt.Sprintf("exit %d", commandExitCodeNonZero),
	})

	require.NoError(t, err)
	assert.Equal(t, JobStateExited, out.State)
	assert.Equal(t, commandExitCodeNonZero, out.ExitCode)
}

func TestRunCommand_ExplicitDirectory(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)
	dir := t.TempDir()

	input := RunCommandInput{
		Purpose:   commandTestPurpose,
		Command:   "pwd",
		Directory: dir,
	}
	out := runCommandRetryingRace(t, executor, input, func(o RunCommandOutput) bool {
		return o.Stdout == ""
	})

	assert.Equal(t, 0, out.ExitCode)
	assert.Equal(t, filepath.Clean(dir), out.Directory)
	assert.Equal(t, filepath.Clean(dir), strings.TrimSpace(out.Stdout))
}

func TestRunCommand_EnvironmentVisibleToChild(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)

	input := RunCommandInput{
		Purpose: commandTestPurpose,
		Command: `printf '%s' "$FOO"`,
		Environment: map[string]string{
			"FOO": "bar",
		},
	}
	out := runCommandRetryingRace(t, executor, input, func(o RunCommandOutput) bool {
		return o.Stdout == ""
	})

	assert.Equal(t, "bar", out.Stdout)
}

// TestRunCommand_StillRunningWhenBoundExpires is the headline behavior of
// this slice: a command still running when the wait bound elapses is left
// running, not killed, and reported as a job handle.
func TestRunCommand_StillRunningWhenBoundExpires(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)

	started := time.Now()
	out, err := executor.RunCommand(context.Background(), RunCommandInput{
		Purpose:        commandTestPurpose,
		Command:        "sleep 5",
		TimeoutSeconds: commandWaitSeconds,
	})
	elapsed := time.Since(started)

	require.NoError(t, err)
	assert.True(t, out.Running)
	assert.Equal(t, JobStateRunning, out.State)
	assert.Less(t, elapsed, commandWaitSlack)
	assert.True(t, processAlive(t, out.PID), "process must still be running")

	job, ok := executor.jobs.Get(out.JobID)
	require.True(t, ok)

	snapshot, ok := executor.jobs.Signal(
		context.Background(), job.ID, JobSignalKill,
	)
	require.True(t, ok)
	assert.Equal(t, JobStateRunning, snapshot.State)

	assert.Eventually(t, func() bool {
		return !processAlive(t, out.PID)
	}, commandEventuallyWait, commandEventuallyTick,
		"killed job must eventually die")
}

func TestRunCommand_Background_ReturnsImmediately(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)

	started := time.Now()
	out, err := executor.RunCommand(context.Background(), RunCommandInput{
		Purpose:    commandTestPurpose,
		Command:    fmt.Sprintf("sleep %d", commandBackgroundSeconds),
		Background: true,
	})
	elapsed := time.Since(started)

	require.NoError(t, err)
	assert.True(t, out.Background)
	assert.True(t, out.Running)
	assert.Equal(t, JobStateRunning, out.State)
	assert.Less(t, elapsed, commandWaitSlack)

	snapshot, ok := executor.jobs.Signal(
		context.Background(), out.JobID, JobSignalKill,
	)
	require.True(t, ok)
	assert.Equal(t, JobStateRunning, snapshot.State)
}

// TestRunCommand_JobSurvivesTurnCancellation proves a job started by one
// turn keeps running after that turn's own context is cancelled: only an
// explicit signal, shutdown, or the process exiting ends it.
func TestRunCommand_JobSurvivesTurnCancellation(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)

	turnCtx, cancel := context.WithCancel(context.Background())

	out, err := executor.RunCommand(turnCtx, RunCommandInput{
		Purpose:        commandTestPurpose,
		Command:        "sleep 5",
		TimeoutSeconds: commandWaitSeconds,
	})
	require.NoError(t, err)
	require.True(t, out.Running)

	cancel()

	// The turn is gone; the job must still be alive and still tracked.
	time.Sleep(commandEventuallyTick * 2)
	assert.True(t, processAlive(t, out.PID), "job must survive turn cancel")

	job, ok := executor.jobs.Get(out.JobID)
	require.True(t, ok)
	assert.Equal(t, JobStateRunning, job.Snapshot().State)

	snapshot, ok := executor.jobs.Signal(
		context.Background(), out.JobID, JobSignalKill,
	)
	require.True(t, ok)
	assert.Equal(t, JobStateRunning, snapshot.State)

	assert.Eventually(t, func() bool {
		return !processAlive(t, out.PID)
	}, commandEventuallyWait, commandEventuallyTick,
		"job must still be killable after cleanup")
}

func TestRunCommand_JobSurvivesTurnCompletingNormally(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)

	// No cancellation: the turn simply finishes and moves on, exactly like
	// a turn that reaches its normal end.
	turnCtx := context.Background()

	out, err := executor.RunCommand(turnCtx, RunCommandInput{
		Purpose:        commandTestPurpose,
		Command:        "sleep 5",
		TimeoutSeconds: commandWaitSeconds,
	})
	require.NoError(t, err)
	require.True(t, out.Running)

	// The job registry is session-scoped, not turn-scoped: the background
	// command must still be running and readable after the turn ends.
	assert.True(t, processAlive(t, out.PID),
		"job must survive normal turn completion")

	job, ok := executor.jobs.Get(out.JobID)
	require.True(t, ok)
	assert.Equal(t, JobStateRunning, job.Snapshot().State)

	readOut, err := executor.ReadJobOutput(
		context.Background(),
		ReadJobOutputInput{JobID: out.JobID},
	)
	require.NoError(t, err)
	assert.True(t, readOut.Found)
	assert.Equal(t, JobStateRunning, readOut.State)

	snapshot, ok := executor.jobs.Signal(
		context.Background(), out.JobID, JobSignalKill,
	)
	require.True(t, ok)
	assert.Equal(t, JobStateRunning, snapshot.State)

	assert.Eventually(t, func() bool {
		return !processAlive(t, out.PID)
	}, commandEventuallyWait, commandEventuallyTick,
		"job must still be killable after cleanup")
}

func TestRunCommand_StdoutOutputDroppedOverBound(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutorWithLimits(t, Limits{
		MaxJobOutputLines: commandTinyOutputLines,
	})

	input := RunCommandInput{
		Purpose: commandTestPurpose,
		Command: fmt.Sprintf(
			"i=0; while [ $i -lt %d ]; do echo line$i; i=$((i+1)); done",
			commandOverflowLineCount,
		),
	}
	out := runCommandRetryingRace(t, executor, input, func(o RunCommandOutput) bool {
		return o.StdoutDroppedLines == 0
	})

	assert.Positive(t, out.StdoutDroppedLines)
	assert.LessOrEqual(t, strings.Count(out.Stdout, "\n")+1, commandTinyOutputLines)
}

func TestRunCommand_BinaryOutput(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)

	input := RunCommandInput{
		Purpose: commandTestPurpose,
		Command: `printf 'A\000B'`,
	}
	out := runCommandRetryingRace(t, executor, input, func(o RunCommandOutput) bool {
		return o.Stdout == ""
	})

	assert.Equal(t, "A\x00B", out.Stdout)
}

func TestRunCommand_ContextAlreadyCancelled(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := executor.RunCommand(ctx, RunCommandInput{
		Purpose: commandTestPurpose,
		Command: "true",
	})

	require.ErrorIs(t, err, context.Canceled)
}

func TestRunCommand_ToolCallIDRecordedOnJob(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)

	const callID = "call-123"

	ctx := ContextWithToolCallID(context.Background(), callID)

	out, err := executor.RunCommand(ctx, RunCommandInput{
		Purpose: commandTestPurpose,
		Command: "true",
	})
	require.NoError(t, err)

	job, ok := executor.jobs.Get(out.JobID)
	require.True(t, ok)
	assert.Equal(t, callID, job.ToolCallID)
}

func TestRunCommand_GracefulStopReachesForkedGrandchild(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutorWithLimits(t, Limits{
		JobStopGracePeriod: commandEventuallyWait,
	})
	marker := filepath.Join(t.TempDir(), "grandchild-pid")

	out, err := executor.RunCommand(context.Background(), RunCommandInput{
		Purpose: commandTestPurpose,
		Command: fmt.Sprintf(
			"sleep 30 & echo $! > %s; wait", marker,
		),
		Background: true,
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		_, statErr := os.Stat(marker)

		return statErr == nil
	}, commandEventuallyWait, commandEventuallyTick,
		"grandchild pid file never appeared")

	grandchildPID := readPIDFile(t, marker)
	require.True(t, processAlive(t, grandchildPID))

	snapshot, ok := executor.jobs.Signal(
		context.Background(), out.JobID, JobSignalStop,
	)
	require.True(t, ok)
	assert.Equal(t, JobStateRunning, snapshot.State)

	assert.Eventually(t, func() bool {
		return !processAlive(t, grandchildPID)
	}, commandEventuallyWait, commandEventuallyTick,
		"forked grandchild must die via the process group")
}

func TestRunCommand_EscalatesToSIGKILLWhenTERMIgnored(t *testing.T) {
	t.Parallel()

	const grace = 300 * time.Millisecond

	executor := newTestJobExecutorWithLimits(t, Limits{
		JobStopGracePeriod: grace,
	})

	out, err := executor.RunCommand(context.Background(), RunCommandInput{
		Purpose:    commandTestPurpose,
		Command:    "trap '' TERM; sleep 30",
		Background: true,
	})
	require.NoError(t, err)

	snapshot, ok := executor.jobs.Signal(
		context.Background(), out.JobID, JobSignalStop,
	)
	require.True(t, ok)
	assert.Equal(t, JobStateRunning, snapshot.State)

	assert.Eventually(t, func() bool {
		return !processAlive(t, out.PID)
	}, commandEventuallyWait, commandEventuallyTick,
		"a TERM-ignoring process must still die via SIGKILL escalation")

	job, ok := executor.jobs.Get(out.JobID)
	require.True(t, ok)

	assert.Eventually(t, func() bool {
		return job.Snapshot().State == JobStateSignalled
	}, commandEventuallyWait, commandEventuallyTick,
		"job must finalize as signalled")
}

func readPIDFile(t *testing.T, path string) int {
	t.Helper()

	content, err := os.ReadFile(path)
	require.NoError(t, err)

	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	require.NoError(t, err)

	return pid
}
