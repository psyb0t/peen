package peen

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/http/api"
)

// Jobs addresses process jobs owned by one session.
type Jobs struct{ session *Session }

// JobListRequest filters a bounded job page by optional state.
type JobListRequest struct {
	PageRequest
	State JobState
}

// CursorRequest selects a bounded window after an event or output sequence.
type CursorRequest struct {
	Cursor int64
	Limit  int
}

// JobOutputRequest selects stdout, stderr or both in durable sequence order.
type JobOutputRequest struct {
	CursorRequest
	Stream JobStream
}

func parseRecordID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed, "invalid record ID",
		)
	}

	return id, nil
}

// Jobs returns this session's process control surface.
func (s *Session) Jobs() Jobs { return Jobs{session: s} }

// List reads jobs and their final state after a restart.
func (j Jobs) List(
	ctx context.Context,
	request JobListRequest,
) (JobPage, error) {
	limit, offset, err := pageParameters(request.PageRequest)
	if err != nil {
		return JobPage{}, err
	}

	return sessionRead[JobPage](ctx, j.session,
		func(ctx context.Context) (any, error) {
			params := api.ListSessionJobsParams{
				XSessionID: j.session.id, Limit: limit, Offset: offset,
			}

			if request.State != "" {
				state := api.ListSessionJobsParamsState(request.State)
				params.State = &state
			}

			return j.session.runtime.internal.ListSessionJobs(ctx,
				j.session.id, params)
		})
}

// Output reads persisted output without waiting for the process to finish.
func (j Jobs) Output(
	ctx context.Context,
	jobID string,
	request JobOutputRequest,
) (JobOutput, error) {
	id, err := parseRecordID(jobID)
	if err != nil {
		return JobOutput{}, err
	}

	limit, _, err := pageParameters(PageRequest{Limit: request.Limit})
	if err != nil {
		return JobOutput{}, err
	}

	return sessionRead[JobOutput](ctx, j.session,
		func(ctx context.Context) (any, error) {
			params := api.ReadSessionJobOutputParams{
				XSessionID: j.session.id, Limit: limit, Cursor: &request.Cursor,
			}
			if request.Stream != "" {
				stream := api.ReadSessionJobOutputParamsStream(request.Stream)
				params.Stream = &stream
			}

			return j.session.runtime.internal.ReadSessionJobOutput(ctx,
				j.session.id, id, params)
		})
}

// Signal requests stop or kill and records even a no-op against an ended job.
func (j Jobs) Signal(
	ctx context.Context,
	jobID string,
	signal JobSignal,
) (JobSignalResponse, error) {
	id, err := parseRecordID(jobID)
	if err != nil {
		return JobSignalResponse{}, err
	}

	return sessionRead[JobSignalResponse](ctx, j.session,
		func(ctx context.Context) (any, error) {
			return j.session.runtime.internal.SignalSessionJob(ctx,
				j.session.id, id,
				api.JobSignalRequest{
					Signal: api.JobSignalRequestSignal(signal),
				})
		})
}

// Signals reads the job's durable signal history.
func (j Jobs) Signals(
	ctx context.Context,
	jobID string,
	request PageRequest,
) (JobSignalRecordPage, error) {
	id, err := parseRecordID(jobID)
	if err != nil {
		return JobSignalRecordPage{}, err
	}

	limit, offset, err := pageParameters(request)
	if err != nil {
		return JobSignalRecordPage{}, err
	}

	return sessionRead[JobSignalRecordPage](ctx, j.session,
		func(ctx context.Context) (any, error) {
			return j.session.runtime.internal.ListSessionJobSignalRequests(ctx,
				j.session.id, id, api.ListSessionJobSignalRequestsParams{
					XSessionID: j.session.id, Limit: limit, Offset: offset,
				})
		})
}
