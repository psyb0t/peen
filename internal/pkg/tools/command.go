package tools

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
)

// RunCommand executes one shell command immediately: no approval step, no
// allowlist or denylist, no sandbox. Isolation is the operator's job, not
// this method's.
//
// A timeout here bounds only how long this call WAITS, never the process's
// life: a command still running when the wait bound elapses, or started
// with Background, is left running and reported as a job handle. Invalid
// input, an invalid working directory, caller cancellation or a launch
// failure returns an error. Non-zero exits are reported in the output.
// Caller cancellation while awaiting a foreground command kills its process
// group. A returned job handle has detached and survives later cancellation.
func (e *JobExecutor) RunCommand(
	ctx context.Context,
	input RunCommandInput,
) (RunCommandOutput, error) {
	if err := ctx.Err(); err != nil {
		return RunCommandOutput{}, ctxerrors.Wrap(err, "runCommand context")
	}

	purpose := strings.TrimSpace(input.Purpose)
	if purpose == "" {
		return RunCommandOutput{}, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"purpose is required")
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

	waitBound, err := e.resolveCommandTimeout(input.Timeout)
	if err != nil {
		return RunCommandOutput{}, err
	}

	env, err := buildCommandEnv(input.Environment)
	if err != nil {
		return RunCommandOutput{}, err
	}

	job, err := e.jobs.Start(ctx, StartJobInput{
		Command:            input.Command,
		Directory:          directory,
		Env:                append(e.commandBaseEnvironment(), env...),
		Purpose:            purpose,
		TurnID:             e.turnID,
		AgentRunID:         e.agentRunID,
		WorkerGenerationID: e.workerGenerationID,
		ToolCallID:         toolCallIDFromContext(ctx),
		Awaited:            !input.Background,
	})
	if err != nil {
		return RunCommandOutput{}, err
	}

	if !input.Background {
		if err := e.waitForForegroundCommand(ctx, job, waitBound); err != nil {
			return jobRunOutput(job, false), err
		}
	}

	e.logJobStarted(ctx, job, input.Background)

	return jobRunOutput(job, input.Background), nil
}

func (e *JobExecutor) waitForForegroundCommand(
	ctx context.Context,
	job *Job,
	bound time.Duration,
) error {
	waitForJob(ctx, job, bound)

	if err := ctx.Err(); err != nil {
		stopErr := e.stopForegroundCommand(ctx, job)

		return ctxerrors.Wrap(errors.Join(err, stopErr),
			"cancel foreground command")
	}

	return nil
}

func (e *JobExecutor) stopForegroundCommand(
	ctx context.Context,
	job *Job,
) error {
	_, _, signalErr := e.jobs.SignalContext(ctx, job.ID, JobSignalKill)
	if signalErr != nil {
		signalErr = ctxerrors.Wrap(signalErr, "record foreground cancellation")

		job.requestKill(context.WithoutCancel(ctx))
	}

	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx),
		e.jobs.limits.JobStopGracePeriod)
	defer cancel()

	select {
	case <-job.Done():
		return signalErr
	case <-cleanup.Done():
		return ctxerrors.Wrap(errors.Join(signalErr, cleanup.Err()),
			"wait for cancelled foreground job")
	}
}

// waitForJob blocks until job leaves the running state or bound elapses,
// whichever comes first. The normal wait bound releases a running job so its
// completion reaches the session as an event. Caller cancellation leaves the
// job's foreground ownership intact for its caller to settle.
func waitForJob(ctx context.Context, job *Job, bound time.Duration) {
	waitCtx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()

	select {
	case <-job.Done():
	case <-waitCtx.Done():
		if ctx.Err() == nil {
			job.Release()
		}
	}
}

// jobRunOutput renders a job's current snapshot and captured output as a
// RunCommandOutput.
func jobRunOutput(job *Job, background bool) RunCommandOutput {
	snapshot := job.Snapshot()
	stdout, stdoutDropped := job.stdout.All()
	stderr, stderrDropped := job.stderr.All()
	running := snapshot.State == JobStateRunning

	output := RunCommandOutput{
		JobID:              snapshot.ID,
		PID:                snapshot.PID,
		Directory:          snapshot.Directory,
		Purpose:            snapshot.Purpose,
		Background:         background,
		Running:            running,
		State:              snapshot.State,
		ExitCode:           snapshot.ExitCode,
		Stdout:             strings.Join(stdout, "\n"),
		Stderr:             strings.Join(stderr, "\n"),
		StdoutDroppedLines: stdoutDropped,
		StderrDroppedLines: stderrDropped,
	}
	if running && !background {
		output.Note = runCommandStillRunningNote
	}

	return output
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

	info, err := e.filesystem.Stat(resolved)
	if err != nil {
		return "", wrapPathError(err, "stat command directory")
	}

	if !info.IsDir() {
		return "", ctxerrors.Wrap(ErrNotDirectory, "command directory")
	}

	return resolved, nil
}

// resolveCommandTimeout returns the wait a runCommand call asked for, or the
// configured default when it asked for none. A call may ask for any wait.
func (e *JobExecutor) resolveCommandTimeout(
	raw string,
) (time.Duration, error) {
	return resolveWaitTimeout(raw, e.Limits().CommandTimeout)
}

// resolveWaitTimeout parses a requested wait, taking fallback when the
// request is empty.
func resolveWaitTimeout(
	raw string,
	fallback time.Duration,
) (time.Duration, error) {
	if raw == "" {
		return fallback, nil
	}

	return parseToolTimeout(raw)
}

// parseToolTimeout parses a non-empty timeout argument as a Go duration and
// requires it to be positive.
func parseToolTimeout(raw string) (time.Duration, error) {
	timeout, err := time.ParseDuration(raw)
	if err != nil {
		return 0, ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"timeout %q must be a Go duration such as 90s, 5m, or 1h30m: %v",
			raw,
			err,
		)
	}

	if timeout <= 0 {
		return 0, ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"timeout %q must be positive",
			raw,
		)
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

	runtimecontext.Logger(ctx).Info(
		"runCommand finished",
		"job_id", snapshot.ID,
		"pid", snapshot.PID,
		"directory", snapshot.Directory,
		"state", snapshot.State,
		"background", background,
		"exit_code", snapshot.ExitCode,
	)
}
