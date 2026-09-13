package agent

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/tools"
)

// ListSessionJobs reads the canonical SQLite job history, newest first.
func (r *Runtime) ListSessionJobs(
	ctx context.Context,
	sessionID uuid.UUID,
	params api.ListSessionJobsParams,
) (*api.JobPage, error) {
	limit, offset, err := pagingOptionsFromAPI(params.Limit, params.Offset)
	if err != nil {
		return nil, err
	}

	options := session.ListJobsOptions{Limit: limit, Offset: offset}

	if params.State != nil {
		state := models.JobState(*params.State)
		options.State = &state
	}

	stored, err := r.store.ListJobs(ctx, sessionID, options)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list durable session jobs")
	}

	pageLimit, err := messagePageValueToAPI(stored.Limit, "job page limit")
	if err != nil {
		return nil, err
	}

	pageOffset, err := messagePageValueToAPI(stored.Offset, "job page offset")
	if err != nil {
		return nil, err
	}

	page := api.JobPage{
		HasMore: stored.HasMore,
		Jobs:    make([]api.Job, 0, len(stored.Items)),
		Limit:   pageLimit,
		Offset:  pageOffset,
	}
	for _, item := range stored.Items {
		converted, convertErr := jobModelToAPI(item)
		if convertErr != nil {
			return nil, convertErr
		}

		page.Jobs = append(page.Jobs, converted)
	}

	return &page, nil
}

// ReadSessionJobOutput replays immutable SQLite output in observed order.
func (r *Runtime) ReadSessionJobOutput(
	ctx context.Context,
	sessionID uuid.UUID,
	jobID uuid.UUID,
	params api.ReadSessionJobOutputParams,
) (*api.JobOutput, error) {
	options, err := jobOutputOptionsFromAPI(params)
	if err != nil {
		return nil, err
	}

	stored, err := r.store.ListJobOutput(ctx, sessionID, jobID, options)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list durable job output")
	}

	state, err := jobStateToAPI(stored.Job.State)
	if err != nil {
		return nil, err
	}

	output := api.JobOutput{
		HasMore:    stored.HasMore,
		JobId:      stored.Job.ID,
		Lines:      make([]api.JobOutputLine, 0, len(stored.Items)),
		NextCursor: stored.NextCursor,
		State:      api.JobOutputState(state),
	}
	for _, item := range stored.Items {
		converted, convertErr := jobOutputLineModelToAPI(item)
		if convertErr != nil {
			return nil, convertErr
		}

		output.Lines = append(output.Lines, converted)
	}

	return &output, nil
}

// ListSessionJobSignalRequests reads every accepted and no-op signal request
// from SQLite. This makes an old session inspectable after its processes have
// exited or Peen has restarted.
func (r *Runtime) ListSessionJobSignalRequests(
	ctx context.Context,
	sessionID uuid.UUID,
	jobID uuid.UUID,
	params api.ListSessionJobSignalRequestsParams,
) (*api.JobSignalRecordPage, error) {
	limit, offset, err := pagingOptionsFromAPI(params.Limit, params.Offset)
	if err != nil {
		return nil, err
	}

	stored, err := r.store.ListJobSignalRequests(
		ctx,
		sessionID,
		jobID,
		session.ListJobSignalRequestsOptions{Limit: limit, Offset: offset},
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list durable job signal requests")
	}

	job, err := jobModelToAPI(stored.Job)
	if err != nil {
		return nil, err
	}

	pageLimit, err := messagePageValueToAPI(
		stored.Limit,
		"job signal page limit",
	)
	if err != nil {
		return nil, err
	}

	pageOffset, err := messagePageValueToAPI(
		stored.Offset,
		"job signal page offset",
	)
	if err != nil {
		return nil, err
	}

	page := api.JobSignalRecordPage{
		HasMore:        stored.HasMore,
		Job:            job,
		Limit:          pageLimit,
		Offset:         pageOffset,
		SignalRequests: make([]api.JobSignalRecord, 0, len(stored.Items)),
	}
	for _, item := range stored.Items {
		converted, convertErr := jobSignalRecordModelToAPI(item)
		if convertErr != nil {
			return nil, convertErr
		}

		page.SignalRequests = append(page.SignalRequests, converted)
	}

	return &page, nil
}

// SignalSessionJob stops a live process while preserving a durable record of
// every accepted or no-op request. A process that is no longer live cannot be
// signalled again, but its row and the no-op request remain inspectable.
func (r *Runtime) SignalSessionJob(
	ctx context.Context,
	sessionID uuid.UUID,
	jobID uuid.UUID,
	request api.JobSignalRequest,
) (*api.JobSignalResponse, error) {
	signal, err := jobSignalFromAPI(request.Signal)
	if err != nil {
		return nil, err
	}

	stored, err := r.store.GetJob(ctx, sessionID, jobID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get durable job for signal")
	}

	registry, err := r.existingSessionJobs(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	if registry == nil {
		return r.recordUnavailableJobSignal(ctx, stored, signal)
	}

	snapshot, found, signalErr := registry.SignalContext(ctx, jobID, signal)
	if signalErr != nil {
		return nil, ctxerrors.Wrap(signalErr, "dispatch job signal")
	}

	if !found {
		return r.recordUnavailableJobSignal(ctx, stored, signal)
	}

	state, err := jobStateToAPI(models.JobState(snapshot.State))
	if err != nil {
		return nil, err
	}

	return &api.JobSignalResponse{
		JobId:     snapshot.ID,
		Signalled: snapshot.State == tools.JobStateRunning,
		State:     api.JobSignalResponseState(state),
	}, nil
}

func (r *Runtime) recordUnavailableJobSignal(
	ctx context.Context,
	stored *models.Job,
	signal tools.JobSignal,
) (*api.JobSignalResponse, error) {
	modelSignal, err := jobSignalToModel(signal)
	if err != nil {
		return nil, err
	}

	if _, err := r.store.RecordJobSignal(
		ctx,
		stored.SessionID,
		stored.ID,
		session.RecordJobSignalInput{Signal: modelSignal, Accepted: false},
	); err != nil {
		return nil, ctxerrors.Wrap(err, "record unavailable job signal")
	}

	state, err := jobStateToAPI(stored.State)
	if err != nil {
		return nil, err
	}

	return &api.JobSignalResponse{
		JobId:     stored.ID,
		Signalled: false,
		State:     api.JobSignalResponseState(state),
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

func jobOutputOptionsFromAPI(
	params api.ReadSessionJobOutputParams,
) (session.ListJobOutputOptions, error) {
	stream, hasStream, err := jobOutputStreamFromAPI(params.Stream)
	if err != nil {
		return session.ListJobOutputOptions{}, err
	}

	limit := session.DefaultPageLimit
	if params.Limit != nil {
		limit = int(*params.Limit)
	}

	options := session.ListJobOutputOptions{
		Cursor: int64OrZero(params.Cursor),
		Limit:  limit,
	}
	if hasStream {
		options.Stream = &stream
	}

	return options, nil
}

func jobOutputStreamFromAPI(
	stream *api.ReadSessionJobOutputParamsStream,
) (models.JobOutputStream, bool, error) {
	if stream == nil {
		return "", false, nil
	}

	switch *stream {
	case api.ReadSessionJobOutputParamsStreamBoth:
		return "", false, nil
	case api.ReadSessionJobOutputParamsStreamStdout:
		return models.JobOutputStreamStdout, true, nil
	case api.ReadSessionJobOutputParamsStreamStderr:
		return models.JobOutputStreamStderr, true, nil
	default:
		return "", false, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"job output stream",
		)
	}
}

func jobModelToAPI(stored *models.Job) (api.Job, error) {
	if stored == nil {
		return api.Job{}, ctxerrors.Wrap(
			commerr.ErrInvalidState,
			"nil durable job",
		)
	}

	state, err := jobStateToAPI(stored.State)
	if err != nil {
		return api.Job{}, err
	}

	result := api.Job{
		Command:       stored.Command,
		Directory:     stored.Directory,
		EndedAt:       stored.EndedAt,
		ExitCode:      stored.ExitCode,
		FailureDetail: stored.FailureDetail,
		JobId:         stored.ID,
		Pid:           stored.PID,
		Purpose:       stored.Purpose,
		SessionId:     stored.SessionID,
		StartedAt:     stored.StartedAt,
		State:         state,
		TurnId:        stored.TurnID,
	}
	if stored.ToolCallID != "" {
		result.ToolCallId = &stored.ToolCallID
	}

	return result, nil
}

func jobOutputLineModelToAPI(
	stored *models.JobOutputLine,
) (api.JobOutputLine, error) {
	if stored == nil {
		return api.JobOutputLine{}, ctxerrors.Wrap(
			commerr.ErrInvalidState,
			"nil durable job output line",
		)
	}

	stream, err := jobOutputLineStreamToAPI(stored.Stream)
	if err != nil {
		return api.JobOutputLine{}, err
	}

	return api.JobOutputLine{
		Content:   stored.Content,
		CreatedAt: stored.CreatedAt,
		Id:        stored.ID,
		JobId:     stored.JobID,
		Sequence:  stored.Sequence,
		SessionId: stored.SessionID,
		Stream:    stream,
	}, nil
}

func jobSignalRecordModelToAPI(
	stored *models.JobSignalRequest,
) (api.JobSignalRecord, error) {
	if stored == nil {
		return api.JobSignalRecord{}, ctxerrors.Wrap(
			commerr.ErrInvalidState,
			"nil durable job signal request",
		)
	}

	state, err := jobStateToAPI(stored.StateAtRequest)
	if err != nil {
		return api.JobSignalRecord{}, err
	}

	return api.JobSignalRecord{
		Accepted:       stored.Accepted,
		Id:             stored.ID,
		JobId:          stored.JobID,
		RequestedAt:    stored.RequestedAt,
		SessionId:      stored.SessionID,
		Signal:         api.JobSignalRecordSignal(stored.Signal),
		StateAtRequest: api.JobSignalRecordStateAtRequest(state),
	}, nil
}

func jobStateToAPI(state models.JobState) (api.JobState, error) {
	switch state {
	case models.JobStateRunning:
		return api.JobStateRunning, nil
	case models.JobStateExited:
		return api.JobStateExited, nil
	case models.JobStateSignalled:
		return api.JobStateSignalled, nil
	case models.JobStateFailed:
		return api.JobStateFailed, nil
	case models.JobStateInterrupted:
		return api.JobStateInterrupted, nil
	default:
		return "", ctxerrors.Wrapf(
			commerr.ErrInvalidState,
			"job state %q",
			state,
		)
	}
}

func jobOutputLineStreamToAPI(
	stream models.JobOutputStream,
) (api.JobOutputLineStream, error) {
	switch stream {
	case models.JobOutputStreamStdout:
		return api.JobOutputLineStreamStdout, nil
	case models.JobOutputStreamStderr:
		return api.JobOutputLineStreamStderr, nil
	default:
		return "", ctxerrors.Wrapf(
			commerr.ErrInvalidState,
			"job output stream %q",
			stream,
		)
	}
}

func int32OrZero(value *int32) int {
	if value == nil {
		return 0
	}

	return int(*value)
}

func int64OrZero(value *int64) int64 {
	if value == nil {
		return 0
	}

	return *value
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

func pageWindow[T any](items []T, offset, limit int) []T {
	if offset >= len(items) {
		return nil
	}

	return items[offset:min(offset+limit, len(items))]
}

func jobSignalFromAPI(
	signal api.JobSignalRequestSignal,
) (tools.JobSignal, error) {
	switch signal {
	case api.JobSignalRequestSignalStop:
		return tools.JobSignalStop, nil
	case api.JobSignalRequestSignalKill:
		return tools.JobSignalKill, nil
	default:
		return "", ctxerrors.Wrap(commerr.ErrValidationFailed, "job signal")
	}
}
