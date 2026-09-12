package tools

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
)

// ListJobs reports this session's jobs, oldest first, bounded by
// Limits.MaxListJobs.
func (e *JobExecutor) ListJobs(
	_ context.Context,
	input ListJobsInput,
) (ListJobsOutput, error) {
	jobs := e.jobs.List()

	summaries := make([]JobSummary, 0, len(jobs))

	for _, job := range jobs {
		snapshot := job.Snapshot()
		if input.State != "" && snapshot.State != input.State {
			continue
		}

		summaries = append(summaries, jobSummaryFrom(snapshot))
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].StartedAt.Before(summaries[j].StartedAt)
	})

	truncated := false

	limit := e.Limits().MaxListJobs
	if len(summaries) > limit {
		summaries = summaries[:limit]
		truncated = true
	}

	return ListJobsOutput{Jobs: summaries, Truncated: truncated}, nil
}

func jobSummaryFrom(snapshot JobSnapshot) JobSummary {
	return JobSummary{
		JobID:               snapshot.ID,
		PID:                 snapshot.PID,
		TurnID:              snapshot.TurnID,
		ToolCallID:          snapshot.ToolCallID,
		Purpose:             snapshot.Purpose,
		Command:             snapshot.Command,
		Directory:           snapshot.Directory,
		State:               snapshot.State,
		StartedAt:           snapshot.StartedAt,
		EndedAt:             snapshot.EndedAt,
		ExitCode:            snapshot.ExitCode,
		StdoutBufferedLines: snapshot.StdoutBufferedLines,
		StdoutDroppedLines:  snapshot.StdoutDroppedLines,
		StderrBufferedLines: snapshot.StderrBufferedLines,
		StderrDroppedLines:  snapshot.StderrDroppedLines,
	}
}

// ReadJobOutput returns a bounded incremental window of one job's output,
// starting at the requested cursors. Found is false when JobID names no job
// in this session, including a job ID that belongs to a different session.
func (e *JobExecutor) ReadJobOutput(
	_ context.Context,
	input ReadJobOutputInput,
) (ReadJobOutputOutput, error) {
	job, ok := e.jobs.Get(input.JobID)
	if !ok {
		return ReadJobOutputOutput{JobID: input.JobID}, nil
	}

	stream, err := resolveJobStream(input.Stream)
	if err != nil {
		return ReadJobOutputOutput{}, err
	}

	maxLines := e.resolveReadJobOutputLines(input.MaxLines)

	output := ReadJobOutputOutput{
		JobID: job.ID,
		Found: true,
		State: job.Snapshot().State,
	}

	if stream == JobStreamStdout || stream == JobStreamBoth {
		lines, next, dropped := job.stdout.Read(input.StdoutCursor, maxLines)
		output.Stdout = strings.Join(lines, "\n")
		output.NextStdoutCursor = next
		output.StdoutDroppedLines = dropped
	}

	if stream == JobStreamStderr || stream == JobStreamBoth {
		lines, next, dropped := job.stderr.Read(input.StderrCursor, maxLines)
		output.Stderr = strings.Join(lines, "\n")
		output.NextStderrCursor = next
		output.StderrDroppedLines = dropped
	}

	return output, nil
}

func resolveJobStream(stream JobStream) (JobStream, error) {
	switch stream {
	case "":
		return JobStreamBoth, nil
	case JobStreamStdout, JobStreamStderr, JobStreamBoth:
		return stream, nil
	default:
		return "", ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"unknown job stream",
		)
	}
}

func (e *JobExecutor) resolveReadJobOutputLines(requested int) int {
	limit := e.Limits().MaxReadJobOutputLines
	if requested <= 0 || requested > limit {
		return limit
	}

	return requested
}

// WaitJob blocks up to a bounded time for one job to leave the running
// state, then reports its status and whatever output arrived while
// waiting. Found is false when JobID names no job in this session.
func (e *JobExecutor) WaitJob(
	ctx context.Context,
	input WaitJobInput,
) (WaitJobOutput, error) {
	job, ok := e.jobs.Get(input.JobID)
	if !ok {
		return WaitJobOutput{JobID: input.JobID}, nil
	}

	bound, err := e.resolveWaitJobTimeout(input.TimeoutSeconds)
	if err != nil {
		return WaitJobOutput{}, err
	}

	waitForJob(ctx, job, bound)

	return jobWaitOutput(job, input), nil
}

func (e *JobExecutor) resolveWaitJobTimeout(
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
		return limits.WaitJobTimeout, nil
	}

	bound := time.Duration(seconds) * time.Second
	if bound > limits.MaxWaitJobTimeout {
		return limits.MaxWaitJobTimeout, nil
	}

	return bound, nil
}

func jobWaitOutput(job *Job, input WaitJobInput) WaitJobOutput {
	snapshot := job.Snapshot()

	stdoutLines, nextStdout, stdoutDropped := job.stdout.Read(
		input.StdoutCursor, 0,
	)
	stderrLines, nextStderr, stderrDropped := job.stderr.Read(
		input.StderrCursor, 0,
	)

	var durationMs int64
	if !snapshot.EndedAt.IsZero() {
		durationMs = snapshot.EndedAt.Sub(snapshot.StartedAt).Milliseconds()
	}

	return WaitJobOutput{
		JobID:              snapshot.ID,
		Found:              true,
		Running:            snapshot.State == JobStateRunning,
		State:              snapshot.State,
		ExitCode:           snapshot.ExitCode,
		DurationMs:         durationMs,
		Stdout:             strings.Join(stdoutLines, "\n"),
		Stderr:             strings.Join(stderrLines, "\n"),
		NextStdoutCursor:   nextStdout,
		NextStderrCursor:   nextStderr,
		StdoutDroppedLines: stdoutDropped,
		StderrDroppedLines: stderrDropped,
	}
}

// SignalJob asks one job's process group to stop or die. Signalling an
// unknown, exited, or already signalled job is idempotent: it reports the
// job's current state rather than failing.
func (e *JobExecutor) SignalJob(
	ctx context.Context,
	input SignalJobInput,
) (SignalJobOutput, error) {
	signal, err := resolveJobSignal(input.Signal)
	if err != nil {
		return SignalJobOutput{}, err
	}

	snapshot, ok, signalErr := e.jobs.SignalContext(ctx, input.JobID, signal)
	if signalErr != nil {
		return SignalJobOutput{}, ctxerrors.Wrap(signalErr, "persist job signal")
	}
	if !ok {
		return SignalJobOutput{JobID: input.JobID, Signal: signal}, nil
	}

	if snapshot.State == JobStateRunning {
		ctxscope.GetLogger(ctx).Info(
			"signal_job dispatched",
			"job_id", snapshot.ID,
			"signal", signal,
		)
	}

	return SignalJobOutput{
		JobID:  snapshot.ID,
		Found:  true,
		Signal: signal,
		State:  snapshot.State,
	}, nil
}

func resolveJobSignal(signal JobSignal) (JobSignal, error) {
	switch signal {
	case JobSignalStop, JobSignalKill:
		return signal, nil
	default:
		return "", ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"unknown job signal",
		)
	}
}
