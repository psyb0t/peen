package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	commandExitCodeNonZero   = 7
	commandTestPurpose       = "test command"
	commandWaitTimeout       = "1s"
	commandWaitSlack         = 4 * time.Second
	commandOverflowBytes     = 100000
	commandEventuallyWait    = 5 * time.Second
	commandEventuallyTick    = 5 * time.Millisecond
	commandTinyOutputLines   = 4
	commandOverflowLineCount = 40
	commandBackgroundSeconds = 30
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

func newTestJobExecutorWithLimits(
	t *testing.T,
	limits Limits,
	generationIDs ...uuid.UUID,
) *JobExecutor {
	t.Helper()

	return newTestJobExecutorWithPublisher(t, nil, limits, generationIDs...)
}

func newTestJobExecutorWithPublisher(
	t *testing.T,
	publisher EventPublisher,
	limits Limits,
	generationIDs ...uuid.UUID,
) *JobExecutor {
	t.Helper()

	executor := newTestExecutorWithLimits(t, limits)

	registry, err := NewJobRegistry(uuid.New(), publisher, limits)
	require.NoError(t, err)

	jobExecutor, err := NewJobExecutor(
		executor,
		registry,
		uuid.New(),
		generationIDs...,
	)
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

func TestResolveCommandTimeout(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)
	limits := executor.Limits()

	testCases := []struct {
		name    string
		timeout string
		want    time.Duration
	}{
		{name: "empty uses the default", timeout: "", want: limits.CommandTimeout},
		{name: "minutes and seconds", timeout: "1m30s", want: 90 * time.Second},
		{name: "seconds", timeout: "90s", want: 90 * time.Second},
		{name: "above the maximum is clamped", timeout: "999h", want: limits.MaxCommandTimeout},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := executor.resolveCommandTimeout(tc.timeout)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

type foregroundSignalObserver struct {
	signalErr  error
	signals    chan JobSignal
	finishGate <-chan struct{}
}

func (*foregroundSignalObserver) JobStarted(context.Context, JobSnapshot) error { return nil }
func (*foregroundSignalObserver) JobOutput(context.Context, JobSnapshot, JobOutputRecord) error {
	return nil
}

func (o *foregroundSignalObserver) JobSignal(_ context.Context, _ JobSnapshot, signal JobSignal) error {
	o.signals <- signal

	return o.signalErr
}

func (o *foregroundSignalObserver) JobFinished(context.Context, JobSnapshot) error {
	if o.finishGate != nil {
		<-o.finishGate
	}

	return nil
}

func TestRunCommand_CancellationKillsForegroundProcessGroup(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name         string
		deadline     bool
		wantErr      error
		signalErr    error
		holdFinished bool
	}{
		{name: "caller cancellation", wantErr: context.Canceled},
		{name: "caller deadline", deadline: true, wantErr: context.DeadlineExceeded},
		{name: "signal persistence failure", wantErr: context.Canceled, signalErr: commerr.ErrFetchFailed},
		{name: "bounded finalization wait", wantErr: context.Canceled, holdFinished: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			publisher := &stubPublisher{}
			executor := newTestJobExecutorWithPublisher(t, publisher, Limits{JobStopGracePeriod: time.Second})
			observer := &foregroundSignalObserver{signalErr: tc.signalErr, signals: make(chan JobSignal, 1)}
			executor.jobs.SetObserver(observer)
			t.Cleanup(func() {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), commandEventuallyWait)
				defer cancel()
				require.NoError(t, executor.jobs.Shutdown(cleanup))
			})
			releaseFinish := func() {}
			if tc.holdFinished {
				gate := make(chan struct{})
				observer.finishGate = gate
				releaseFinish = sync.OnceFunc(func() { close(gate) })
				t.Cleanup(releaseFinish)
			}
			unrelated, err := executor.RunCommand(t.Context(), RunCommandInput{Purpose: commandTestPurpose, Command: "sleep 30", Background: true})
			require.NoError(t, err)
			marker := filepath.Join(t.TempDir(), "foreground-child-pid")
			command := fmt.Sprintf("sleep 30 & echo $! > %s; wait", marker)
			ctx, cancel := context.WithCancel(t.Context())
			if tc.deadline {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), time.Second)
			}
			defer cancel()
			finished := make(chan error, 1)
			go func() {
				defer func() {
					if recover() != nil {
						finished <- ctxerrors.New("foreground test panicked")
					}
				}()
				_, err := executor.RunCommand(ctx, RunCommandInput{Purpose: commandTestPurpose, Command: command, Timeout: "30s"})
				finished <- err
			}()
			var childPID int
			require.Eventually(t, func() bool {
				content, err := os.ReadFile(marker)
				if err != nil {
					return false
				}
				childPID, err = strconv.Atoi(strings.TrimSpace(string(content)))

				return err == nil && processAlive(t, childPID)
			}, commandEventuallyWait, commandEventuallyTick)
			var foreground *Job
			require.Eventually(t, func() bool {
				for _, job := range executor.jobs.List() {
					if job.Command == command {
						foreground = job

						return true
					}
				}

				return false
			}, commandEventuallyWait, commandEventuallyTick)
			parentPID := foreground.Snapshot().PID
			if !tc.deadline {
				cancel()
			}
			select {
			case err := <-finished:
				require.ErrorIs(t, err, tc.wantErr)
				if tc.signalErr != nil {
					assert.ErrorIs(t, err, tc.signalErr)
				}
				if tc.holdFinished {
					assert.ErrorIs(t, err, context.DeadlineExceeded)
				}
			case <-time.After(commandEventuallyWait):
				t.Fatal("cancelled foreground command did not return")
			}
			releaseFinish()
			select {
			case signal := <-observer.signals:
				assert.Equal(t, JobSignalKill, signal)
			default:
				t.Fatal("foreground cancellation did not record its kill request")
			}
			assert.False(t, processAlive(t, parentPID), "foreground shell remains alive")
			assert.Eventually(t, func() bool { return !processAlive(t, childPID) }, commandEventuallyWait, commandEventuallyTick, "foreground child remains alive")
			assert.NotEqual(t, JobStateRunning, foreground.Snapshot().State)
			assert.Empty(t, publisher.all(), "cancelled foreground work must not publish a detached completion")
			assert.True(t, processAlive(t, unrelated.PID), "an unrelated detached job must keep running")
		})
	}
}

func TestResolveCommandTimeout_RejectsInvalidDurations(t *testing.T) {
	t.Parallel()

	executor := newTestJobExecutor(t)

	for _, timeout := range []string{"-1s", "0", "0s", "90", "soon", "1d", " "} {
		t.Run(timeout, func(t *testing.T) {
			t.Parallel()

			_, err := executor.resolveCommandTimeout(timeout)

			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
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
				Purpose: commandTestPurpose,
				Command: "true",
				Timeout: "-1s",
			},
		},
		{
			name: "zero timeout",
			input: RunCommandInput{
				Purpose: commandTestPurpose,
				Command: "true",
				Timeout: "0s",
			},
		},
		{
			name: "unparsable timeout",
			input: RunCommandInput{
				Purpose: commandTestPurpose,
				Command: "true",
				Timeout: "soon",
			},
		},
		{
			name: "timeout without a unit",
			input: RunCommandInput{
				Purpose: commandTestPurpose,
				Command: "true",
				Timeout: "90",
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

// Options.Environment is the whole base environment of every command. A
// variable only the process has never reaches the command, the configured base
// does, and the request's own additions still apply.
//
// It sets a process variable, so it does not run in parallel.
func TestRunCommand_UsesTheConfiguredBaseEnvironment(t *testing.T) {
	const (
		processOnlyName = "PEEN_TEST_PROCESS_ONLY_VALUE"
		keptName        = "PEEN_TEST_KEPT_VALUE"
		addedName       = "PEEN_TEST_REQUEST_ADDITION"
	)

	t.Setenv(processOnlyName, "process-only")

	executor, err := NewExecutor(Options{
		Workspace: t.TempDir(),
		Environment: []string{
			"PATH=" + os.Getenv("PATH"),
			keptName + "=kept",
		},
	})
	require.NoError(t, err)

	registry, err := NewJobRegistry(uuid.New(), nil, Limits{})
	require.NoError(t, err)

	jobExecutor, err := NewJobExecutor(executor, registry, uuid.New())
	require.NoError(t, err)

	out, err := jobExecutor.RunCommand(context.Background(), RunCommandInput{
		Purpose:     commandTestPurpose,
		Command:     "env",
		Environment: map[string]string{addedName: "added"},
	})
	require.NoError(t, err)

	assert.Equal(t, 0, out.ExitCode)
	assert.Contains(t, out.Stdout, keptName+"=kept")
	assert.Contains(t, out.Stdout, addedName+"=added")
	assert.NotContains(t, out.Stdout, processOnlyName)
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
	out, err := executor.RunCommand(context.Background(), input)
	require.NoError(t, err)

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
	out, err := executor.RunCommand(context.Background(), input)
	require.NoError(t, err)

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
	out, err := executor.RunCommand(context.Background(), input)
	require.NoError(t, err)

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
	out, err := executor.RunCommand(context.Background(), input)
	require.NoError(t, err)

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
		Purpose: commandTestPurpose,
		Command: "sleep 5",
		Timeout: commandWaitTimeout,
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

// A job's completion event is for work that outlived its runCommand call.
// A command that finishes inside the call already reports its result there,
// so publishing an event too would hand the model the same news twice.
func TestRunCommand_PublishesCompletionOnlyForJobsThatOutliveTheCall(
	t *testing.T,
) {
	t.Parallel()

	testCases := []struct {
		name       string
		input      RunCommandInput
		wantEvents int
	}{
		{
			name:       "finishes within the wait",
			input:      RunCommandInput{Command: "true"},
			wantEvents: 0,
		},
		{
			name: "still running when the wait ends",
			input: RunCommandInput{
				Command: "sleep 2",
				Timeout: commandWaitTimeout,
			},
			wantEvents: 1,
		},
		{
			name:       "started in the background",
			input:      RunCommandInput{Command: "true", Background: true},
			wantEvents: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			publisher := &stubPublisher{}
			executor := newTestJobExecutorWithPublisher(t, publisher, Limits{})

			input := tc.input
			input.Purpose = commandTestPurpose
			out, err := executor.RunCommand(context.Background(), input)
			require.NoError(t, err)

			job, ok := executor.jobs.Get(out.JobID)
			require.True(t, ok)
			select {
			case <-job.Done():
			case <-time.After(commandEventuallyWait):
				t.Fatal("job never finished")
			}

			notices := publisher.all()
			require.Len(t, notices, tc.wantEvents)
			for _, notice := range notices {
				assert.Equal(t, events.TypeJobExited, notice.Type)
			}
		})
	}
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
		Purpose: commandTestPurpose,
		Command: "sleep 5",
		Timeout: commandWaitTimeout,
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
		Purpose: commandTestPurpose,
		Command: "sleep 5",
		Timeout: commandWaitTimeout,
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
	out, err := executor.RunCommand(context.Background(), input)
	require.NoError(t, err)

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
	out, err := executor.RunCommand(context.Background(), input)
	require.NoError(t, err)

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

	generationID := uuid.New()
	executor := newTestJobExecutorWithLimits(t, Limits{}, generationID)

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
	assert.Equal(t, generationID.String(), job.WorkerGenerationID)
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

	grandchildPID := 0
	require.Eventually(t, func() bool {
		content, readErr := os.ReadFile(marker)
		if readErr != nil {
			return false
		}

		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(content)))
		if parseErr != nil {
			return false
		}

		if !processAlive(t, pid) {
			return false
		}

		grandchildPID = pid

		return true
	}, commandEventuallyWait, commandEventuallyTick,
		"grandchild pid never became readable and live")

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
