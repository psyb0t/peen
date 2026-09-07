package tools

import (
	"context"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
)

// RunCommand executes one shell command immediately: no approval step, no
// allowlist or denylist, no sandbox. Isolation is the operator's job, not
// this method's.
//
// A timeout here bounds only how long this call WAITS, never the process's
// life: a command still running when the wait bound elapses, or started
// with Background, is left running and reported as a job handle. Only
// invalid input, a missing or non-directory working directory, caller
// cancellation before the process starts, or a failure to launch return an
// error; a non-zero exit or a job left running is reported in the output.
func (e *JobExecutor) RunCommand(
	ctx context.Context,
	input RunCommandInput,
) (RunCommandOutput, error) {
	if err := ctx.Err(); err != nil {
		return RunCommandOutput{}, ctxerrors.Wrap(err, "run_command context")
	}

	purpose := strings.TrimSpace(input.Purpose)
	if purpose == "" {
		return RunCommandOutput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"purpose is required",
		)
	}

	if strings.TrimSpace(input.Command) == "" {
		return RunCommandOutput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"command is required",
		)
	}

	directory, err := e.resolveCommandDirectory(input.Directory)
	if err != nil {
		return RunCommandOutput{}, err
	}

	waitBound, err := e.resolveCommandTimeout(input.TimeoutSeconds)
	if err != nil {
		return RunCommandOutput{}, err
	}

	env, err := buildCommandEnv(input.Environment)
	if err != nil {
		return RunCommandOutput{}, err
	}

	job, err := e.jobs.Start(ctx, StartJobInput{
		Command:    input.Command,
		Directory:  directory,
		Env:        append(os.Environ(), env...),
		Purpose:    purpose,
		TurnID:     e.turnID,
		ToolCallID: toolCallIDFromContext(ctx),
	})
	if err != nil {
		return RunCommandOutput{}, err
	}

	if !input.Background {
		waitForJob(ctx, job, waitBound)
	}

	e.logJobStarted(ctx, job, input.Background)

	return jobRunOutput(job, input.Background), nil
}

// waitForJob blocks until job leaves the running state or bound elapses,
// whichever comes first. Neither outcome kills job: the bound only ends
// this call's wait.
func waitForJob(ctx context.Context, job *Job, bound time.Duration) {
	waitCtx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()

	select {
	case <-job.Done():
	case <-waitCtx.Done():
	}
}

// jobRunOutput renders a job's current snapshot and captured output as a
// RunCommandOutput.
func jobRunOutput(job *Job, background bool) RunCommandOutput {
	snapshot := job.Snapshot()
	stdout, stdoutDropped := job.stdout.All()
	stderr, stderrDropped := job.stderr.All()

	return RunCommandOutput{
		JobID:              snapshot.ID,
		PID:                snapshot.PID,
		Directory:          snapshot.Directory,
		Purpose:            snapshot.Purpose,
		Background:         background,
		Running:            snapshot.State == JobStateRunning,
		State:              snapshot.State,
		ExitCode:           snapshot.ExitCode,
		Stdout:             strings.Join(stdout, "\n"),
		Stderr:             strings.Join(stderr, "\n"),
		StdoutDroppedLines: stdoutDropped,
		StderrDroppedLines: stderrDropped,
	}
}

// resolveCommandDirectory maps input.Directory onto the host filesystem and
// requires it to already exist as a directory.
func (e *JobExecutor) resolveCommandDirectory(
	directory string,
) (string, error) {
	resolved, err := e.resolvePath(directory)
	if err != nil {
		return "", ctxerrors.Wrap(err, "resolve command directory")
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", wrapPathError(err, "stat command directory")
	}

	if !info.IsDir() {
		return "", ctxerrors.Wrap(ErrNotDirectory, "command directory")
	}

	return resolved, nil
}

// resolveCommandTimeout maps the requested seconds onto a bounded wait
// duration. Zero takes the configured default; anything above the
// configured maximum is clamped down to it.
func (e *JobExecutor) resolveCommandTimeout(
	seconds int,
) (time.Duration, error) {
	if seconds < 0 {
		return 0, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"timeout seconds must not be negative",
		)
	}

	limits := e.Limits()
	if seconds == 0 {
		return limits.CommandTimeout, nil
	}

	timeout := time.Duration(seconds) * time.Second
	if timeout > limits.MaxCommandTimeout {
		return limits.MaxCommandTimeout, nil
	}

	return timeout, nil
}

// buildCommandEnv renders the requested additions as sorted "KEY=VALUE"
// entries so the child process environment is deterministic across calls.
func buildCommandEnv(entries map[string]string) ([]string, error) {
	keys := make([]string, 0, len(entries))

	for key := range entries {
		if err := validateEnvKey(key); err != nil {
			return nil, err
		}

		keys = append(keys, key)
	}

	sort.Strings(keys)

	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+entries[key])
	}

	return env, nil
}

func validateEnvKey(key string) error {
	if key == "" || strings.Contains(key, "=") {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"environment key must be non-empty and not contain '='",
		)
	}

	return nil
}

// logJobStarted emits the one metadata line this call is allowed: never the
// command text, its output, or any environment key or value.
func (e *JobExecutor) logJobStarted(
	ctx context.Context,
	job *Job,
	background bool,
) {
	snapshot := job.Snapshot()

	ctxscope.GetLogger(ctx).Info(
		"run_command finished",
		"job_id", snapshot.ID,
		"pid", snapshot.PID,
		"directory", snapshot.Directory,
		"state", snapshot.State,
		"background", background,
		"exit_code", snapshot.ExitCode,
	)
}
