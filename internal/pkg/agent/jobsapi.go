package agent

import (
	"context"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/tools"
)

const jobOutputLineSeparator = "\n"

// ListSessionJobs reports the commands a session has started, newest first,
// with the same bounded limit/offset paging as GET /v1/messages.
// A session with no registry has simply never run a command, which is an empty
// list rather than an error.
func (r *Runtime) ListSessionJobs(
	ctx context.Context,
	sessionID uuid.UUID,
	params api.ListSessionJobsParams,
) (*api.JobPage, error) {
	registry, err := r.existingSessionJobs(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	page := api.JobPage{Jobs: []api.Job{}, Truncated: false}
	if registry == nil {
		return &page, nil
	}

	limit, offset, err := pagingOptionsFromAPI(params.Limit, params.Offset)
	if err != nil {
		return nil, err
	}

	wanted := ""
	if params.State != nil {
		wanted = string(*params.State)
	}

	snapshots := jobSnapshots(registry)
	filtered := make([]tools.JobSnapshot, 0, len(snapshots))

	for _, snapshot := range snapshots {
		if wanted != "" && snapshot.State != wanted {
			continue
		}

		filtered = append(filtered, snapshot)
	}

	page.Truncated = offset+limit < len(filtered)

	for _, snapshot := range pageWindow(filtered, offset, limit) {
		page.Jobs = append(page.Jobs, jobToAPI(snapshot))
	}

	return &page, nil
}

// ReadSessionJobOutput returns a bounded window of one job's output without
// waiting for it to finish.
func (r *Runtime) ReadSessionJobOutput(
	ctx context.Context,
	sessionID uuid.UUID,
	jobID uuid.UUID,
	params api.ReadSessionJobOutputParams,
) (*api.JobOutput, error) {
	job, err := r.sessionJob(ctx, sessionID, jobID)
	if err != nil {
		return nil, err
	}

	snapshot := job.Snapshot()
	stream := jobStreamOrBoth(params.Stream)
	maxLines := int32OrZero(params.MaxLines)

	output := api.JobOutput{
		JobId: snapshot.ID,
		State: api.JobOutputState(snapshot.State),
	}

	if stream == tools.JobStreamStdout || stream == tools.JobStreamBoth {
		lines, next, dropped := job.ReadStdout(
			int32OrZero(params.StdoutCursor),
			maxLines,
		)
		output.Stdout = strings.Join(lines, jobOutputLineSeparator)
		output.NextStdoutCursor = int32(next)      //nolint:gosec // Bounded.
		output.StdoutDroppedLines = int32(dropped) //nolint:gosec // Bounded.
	}

	if stream == tools.JobStreamStderr || stream == tools.JobStreamBoth {
		lines, next, dropped := job.ReadStderr(
			int32OrZero(params.StderrCursor),
			maxLines,
		)
		output.Stderr = strings.Join(lines, jobOutputLineSeparator)
		output.NextStderrCursor = int32(next)      //nolint:gosec // Bounded.
		output.StderrDroppedLines = int32(dropped) //nolint:gosec // Bounded.
	}

	return &output, nil
}

// SignalSessionJob stops one job. Signalling an unknown, exited, or already
// signalled job is idempotent and reports the current state rather than
// failing, matching POST /v1/session/cancel.
func (r *Runtime) SignalSessionJob(
	ctx context.Context,
	sessionID uuid.UUID,
	jobID uuid.UUID,
	request api.JobSignalRequest,
) (*api.JobSignalResponse, error) {
	registry, err := r.existingSessionJobs(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	if registry == nil {
		return nil, ctxerrors.Wrap(commerr.ErrNotFound, "job")
	}

	signal, err := jobSignalFromAPI(request.Signal)
	if err != nil {
		return nil, err
	}

	snapshot, found := registry.Signal(ctx, jobID, signal)
	if !found {
		return nil, ctxerrors.Wrap(commerr.ErrNotFound, "job")
	}

	return &api.JobSignalResponse{
		JobId:     snapshot.ID,
		Signalled: snapshot.State == tools.JobStateRunning,
		State:     api.JobSignalResponseState(snapshot.State),
	}, nil
}

// existingSessionJobs returns the session's registry WITHOUT creating one.
// Creating a registry from a read would let any GET allocate state.
func (r *Runtime) existingSessionJobs(
	ctx context.Context,
	sessionID uuid.UUID,
) (*tools.JobRegistry, error) {
	if _, err := r.store.Get(ctx, sessionID); err != nil {
		return nil, ctxerrors.Wrap(err, "resolve job session")
	}

	r.jobsMutex.Lock()
	defer r.jobsMutex.Unlock()

	return r.jobs[sessionID], nil
}

func (r *Runtime) sessionJob(
	ctx context.Context,
	sessionID uuid.UUID,
	jobID uuid.UUID,
) (*tools.Job, error) {
	registry, err := r.existingSessionJobs(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	if registry == nil {
		return nil, ctxerrors.Wrap(commerr.ErrNotFound, "job")
	}

	job, ok := registry.Get(jobID)
	if !ok {
		return nil, ctxerrors.Wrap(commerr.ErrNotFound, "job")
	}

	return job, nil
}

// jobSnapshots returns every job newest first, so a caller sees what it just
// started at the top.
func jobSnapshots(registry *tools.JobRegistry) []tools.JobSnapshot {
	jobs := registry.List()
	snapshots := make([]tools.JobSnapshot, 0, len(jobs))

	for _, job := range jobs {
		snapshots = append(snapshots, job.Snapshot())
	}

	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].StartedAt.After(snapshots[j].StartedAt)
	})

	return snapshots
}

// jobToAPI converts one snapshot. Every int32 narrowing here is of a bounded
// counter or a PID, none of which can exceed int32 in practice.
//
//nolint:gosec // See above: bounded counters and a PID.
func jobToAPI(snapshot tools.JobSnapshot) api.Job {
	job := api.Job{
		JobId:               snapshot.ID,
		Pid:                 int32(snapshot.PID),
		Purpose:             snapshot.Purpose,
		Command:             snapshot.Command,
		Directory:           snapshot.Directory,
		State:               api.JobState(snapshot.State),
		StartedAt:           snapshot.StartedAt,
		ExitCode:            int32(snapshot.ExitCode),
		StdoutBufferedLines: int32(snapshot.StdoutBufferedLines),
		StdoutDroppedLines:  int32(snapshot.StdoutDroppedLines),
		StderrBufferedLines: int32(snapshot.StderrBufferedLines),
		StderrDroppedLines:  int32(snapshot.StderrDroppedLines),
	}

	if snapshot.ToolCallID != "" {
		job.ToolCallId = &snapshot.ToolCallID
	}

	if !snapshot.EndedAt.IsZero() {
		ended := snapshot.EndedAt
		job.EndedAt = &ended
	}

	return job
}

func jobStreamOrBoth(stream *api.ReadSessionJobOutputParamsStream) string {
	if stream == nil {
		return tools.JobStreamBoth
	}

	return string(*stream)
}

func int32OrZero(value *int32) int {
	if value == nil {
		return 0
	}

	return int(*value)
}

// pagingOptionsFromAPI resolves bounded limit/offset from optional query
// parameters, defaulting and validating exactly like GET /v1/messages:
// limit defaults to session.DefaultPageLimit and must stay within
// session.MaximumPageLimit; offset defaults to 0 and must not be negative.
func pagingOptionsFromAPI(limit, offset *int32) (int, int, error) {
	resolvedLimit := session.DefaultPageLimit
	if limit != nil {
		resolvedLimit = int(*limit)
	}

	resolvedOffset := 0
	if offset != nil {
		resolvedOffset = int(*offset)
	}

	if resolvedLimit < 1 || resolvedLimit > session.MaximumPageLimit ||
		resolvedOffset < 0 {
		return 0, 0, ctxerrors.Wrap(commerr.ErrValidationFailed, "page")
	}

	return resolvedLimit, resolvedOffset, nil
}

// pageWindow returns the bounded slice of items starting at offset for at
// most limit entries. An offset at or past the end returns an empty slice
// rather than panicking.
func pageWindow[T any](items []T, offset, limit int) []T {
	if offset >= len(items) {
		return nil
	}

	end := min(offset+limit, len(items))

	return items[offset:end]
}

func jobSignalFromAPI(signal api.JobSignalRequestSignal) (string, error) {
	switch string(signal) {
	case tools.JobSignalStop, tools.JobSignalKill:
		return string(signal), nil
	default:
		return "", ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"unknown job signal",
		)
	}
}
