package session

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

const interruptedJobFailureDetail = "process stopped before job completed"

// CreateJob records a supervised process before its output reaches memory or
// any connected client.
func (s *Store) CreateJob(
	ctx context.Context,
	sessionID uuid.UUID,
	input CreateJobInput,
) (*models.Job, error) {
	if err := validateCreateJobInput(sessionID, input); err != nil {
		return nil, err
	}

	jobID := input.ID
	if jobID == uuid.Nil {
		jobID = s.newID()
	}

	startedAt := input.StartedAt
	if startedAt.IsZero() {
		startedAt = s.now()
	} else {
		startedAt = startedAt.UTC()
	}

	result := &models.Job{
		ID:         jobID,
		SessionID:  sessionID,
		TurnID:     input.TurnID,
		ToolCallID: input.ToolCallID,
		PID:        input.PID,
		Purpose:    input.Purpose,
		Command:    input.Command,
		Directory:  input.Directory,
		State:      models.JobStateRunning,
		ExitCode:   -1,
		StartedAt:  startedAt,
	}

	if err := s.query.Transaction(func(tx *repositories.Query) error {
		if _, err := s.findSessionWithQuery(ctx, tx, sessionID); err != nil {
			return err
		}

		turn := tx.Turn
		if _, err := turn.WithContext(ctx).
			Where(
				turn.ID.Eq(input.TurnID),
				turn.SessionID.Eq(sessionID),
			).
			First(); err != nil {
			return ctxerrors.Wrap(err, "find parent turn for job")
		}

		if err := tx.Job.WithContext(ctx).Create(result); err != nil {
			return ctxerrors.Wrap(err, "create job")
		}

		return nil
	}); err != nil {
		return nil, ctxerrors.Wrap(err, "create durable job")
	}

	return result, nil
}

// AppendJobOutput stores one process line in sequence before any live reader
// can see it. stdout and stderr share the same sequence so replay preserves
// observed ordering across both streams.
func (s *Store) AppendJobOutput(
	ctx context.Context,
	sessionID uuid.UUID,
	jobID uuid.UUID,
	input AppendJobOutputInput,
) (*models.JobOutputLine, error) {
	if err := validateJobOutputInput(sessionID, jobID, input); err != nil {
		return nil, err
	}

	input = s.normalizeJobOutputInput(input)

	var result *models.JobOutputLine

	if err := s.query.Transaction(func(tx *repositories.Query) error {
		job, err := s.findJobWithQuery(ctx, tx, sessionID, jobID)
		if err != nil {
			return err
		}

		if job.State != models.JobStateRunning {
			return ctxerrors.Wrap(
				commerr.ErrInvalidState,
				"job output after terminal state",
			)
		}

		line := tx.JobOutputLine

		sequence, err := nextJobOutputSequence(ctx, tx, jobID)
		if err != nil {
			return ctxerrors.Wrap(err, "find latest job output line")
		}

		result = &models.JobOutputLine{
			ID:        input.ID,
			SessionID: sessionID,
			JobID:     jobID,
			Sequence:  sequence,
			Stream:    input.Stream,
			Content:   input.Content,
			CreatedAt: input.CreatedAt,
		}
		if err := line.WithContext(ctx).Create(result); err != nil {
			return ctxerrors.Wrap(err, "create job output line")
		}

		return nil
	}); err != nil {
		return nil, ctxerrors.Wrap(err, "append durable job output")
	}

	return result, nil
}

func nextJobOutputSequence(
	ctx context.Context,
	query *repositories.Query,
	jobID uuid.UUID,
) (int64, error) {
	line := query.JobOutputLine

	return nextSequence(func() (int64, error) {
		latest, err := line.WithContext(ctx).
			Where(line.JobID.Eq(jobID)).
			Order(line.Sequence.Desc(), line.ID.Desc()).
			First()
		if err != nil {
			return 0, ctxerrors.Wrap(err, "query latest job output line")
		}

		return latest.Sequence, nil
	})
}

func (s *Store) normalizeJobOutputInput(
	input AppendJobOutputInput,
) AppendJobOutputInput {
	if input.ID == uuid.Nil {
		input.ID = s.newID()
	}

	if input.CreatedAt.IsZero() {
		input.CreatedAt = s.now()
	} else {
		input.CreatedAt = input.CreatedAt.UTC()
	}

	return input
}

// FinalizeJob records a terminal process result exactly once.
func (s *Store) FinalizeJob(
	ctx context.Context,
	sessionID uuid.UUID,
	jobID uuid.UUID,
	input FinalizeJobInput,
) (*models.Job, error) {
	if !isJobTerminalState(input.State) {
		return nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"job terminal state",
		)
	}

	var result *models.Job

	if err := s.query.Transaction(func(tx *repositories.Query) error {
		finalized, finalizeErr := s.finalizeJobInTransaction(
			ctx,
			tx,
			sessionID,
			jobID,
			input,
		)
		if finalizeErr == nil {
			result = finalized
		}

		return finalizeErr
	}); err != nil {
		return nil, ctxerrors.Wrap(err, "finalize durable job")
	}

	return result, nil
}

func (s *Store) finalizeJobInTransaction(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	jobID uuid.UUID,
	input FinalizeJobInput,
) (*models.Job, error) {
	job, err := s.findJobWithQuery(ctx, query, sessionID, jobID)
	if err != nil {
		return nil, err
	}

	if job.State != models.JobStateRunning {
		return nil, ctxerrors.Wrap(
			commerr.ErrInvalidState,
			"job is not running",
		)
	}

	now := s.now()
	if err := updateJobFinalization(
		ctx,
		query,
		sessionID,
		jobID,
		input,
		now,
	); err != nil {
		return nil, err
	}

	job.State = input.State
	job.ExitCode = input.ExitCode
	job.FailureDetail = input.FailureDetail
	job.EndedAt = &now

	return job, nil
}

func updateJobFinalization(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	jobID uuid.UUID,
	input FinalizeJobInput,
	endedAt time.Time,
) error {
	repository := query.Job

	updated, err := repository.WithContext(ctx).
		Where(
			repository.ID.Eq(jobID),
			repository.SessionID.Eq(sessionID),
			repository.State.Eq(string(models.JobStateRunning)),
		).
		UpdateSimple(
			repository.State.Value(string(input.State)),
			repository.ExitCode.Value(input.ExitCode),
			repository.FailureDetail.Value(input.FailureDetail),
			repository.EndedAt.Value(endedAt),
		)
	if err != nil {
		return ctxerrors.Wrap(err, "finalize job")
	}

	if updated.RowsAffected != 1 {
		return ctxerrors.Wrap(commerr.ErrInvalidState, "job terminal update")
	}

	return nil
}

// RecordJobSignal stores every accepted and no-op signal request.
func (s *Store) RecordJobSignal(
	ctx context.Context,
	sessionID uuid.UUID,
	jobID uuid.UUID,
	input RecordJobSignalInput,
) (*models.JobSignalRequest, error) {
	if err := validateJobSignalInput(sessionID, jobID, input); err != nil {
		return nil, err
	}

	requestID := input.ID
	if requestID == uuid.Nil {
		requestID = s.newID()
	}

	requestedAt := input.RequestedAt
	if requestedAt.IsZero() {
		requestedAt = s.now()
	} else {
		requestedAt = requestedAt.UTC()
	}

	var result *models.JobSignalRequest

	if err := s.query.Transaction(func(tx *repositories.Query) error {
		job, err := s.findJobWithQuery(ctx, tx, sessionID, jobID)
		if err != nil {
			return err
		}

		result = &models.JobSignalRequest{
			ID:             requestID,
			SessionID:      sessionID,
			JobID:          jobID,
			Signal:         input.Signal,
			Accepted:       input.Accepted,
			StateAtRequest: job.State,
			RequestedAt:    requestedAt,
		}
		if err := tx.JobSignalRequest.WithContext(ctx).
			Create(result); err != nil {
			return ctxerrors.Wrap(err, "create job signal request")
		}

		return nil
	}); err != nil {
		return nil, ctxerrors.Wrap(err, "record durable job signal")
	}

	return result, nil
}

// GetJob reads a single durable process job scoped to its session.
func (s *Store) GetJob(
	ctx context.Context,
	sessionID uuid.UUID,
	jobID uuid.UUID,
) (*models.Job, error) {
	job, err := s.findJobWithQuery(ctx, s.query, sessionID, jobID)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get job")
	}

	return job, nil
}

// ListJobs returns a stable newest-first session-local process-job page.
//
//nolint:dupl // The generated Job query has a distinct typed builder.
func (s *Store) ListJobs(
	ctx context.Context,
	sessionID uuid.UUID,
	options ListJobsOptions,
) (*JobPage, error) {
	options, err := normalizeJobListOptions(options)
	if err != nil {
		return nil, err
	}

	if _, err := s.findSession(ctx, sessionID); err != nil {
		return nil, ctxerrors.Wrap(err, "find session for job listing")
	}

	job := s.query.Job

	query := job.WithContext(ctx).Where(job.SessionID.Eq(sessionID))
	if options.State != nil {
		query = query.Where(job.State.Eq(string(*options.State)))
	}

	page := readPage{limit: options.Limit, offset: options.Offset}

	items, hasMore, err := listReadPage(
		page,
		func(offset, limit int) ([]*models.Job, error) {
			return query.
				Order(job.StartedAt.Desc(), job.ID.Desc()).
				Offset(offset).
				Limit(limit).
				Find()
		},
		"list jobs",
		"probe job page continuation",
	)
	if err != nil {
		return nil, err
	}

	return &JobPage{
		Items:   items,
		Limit:   options.Limit,
		Offset:  options.Offset,
		HasMore: hasMore,
	}, nil
}

// ListJobOutput replays the one persisted combined process stream.
func (s *Store) ListJobOutput(
	ctx context.Context,
	sessionID uuid.UUID,
	jobID uuid.UUID,
	options ListJobOutputOptions,
) (*JobOutputPage, error) {
	options, err := normalizeJobOutputOptions(options)
	if err != nil {
		return nil, err
	}

	job, err := s.GetJob(ctx, sessionID, jobID)
	if err != nil {
		return nil, err
	}

	line := s.query.JobOutputLine

	query := line.WithContext(ctx).
		Where(
			line.SessionID.Eq(sessionID),
			line.JobID.Eq(jobID),
			line.Sequence.Gte(options.Cursor+1),
		).
		Order(line.Sequence.Asc(), line.ID.Asc())
	if options.Stream != nil {
		query = query.Where(line.Stream.Eq(string(*options.Stream)))
	}

	items, err := query.Limit(options.Limit).Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list job output")
	}

	nextCursor := options.Cursor
	if len(items) > 0 {
		nextCursor = items[len(items)-1].Sequence
	}

	probe, err := query.Offset(len(items)).Limit(1).Find()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "probe job output continuation")
	}

	return &JobOutputPage{
		Job:        job,
		Items:      items,
		NextCursor: nextCursor,
		HasMore:    len(probe) > 0,
	}, nil
}

// ListJobSignalRequests returns every durable request to signal one job.
//
//nolint:dupl // Generated signal request query has a distinct typed builder.
func (s *Store) ListJobSignalRequests(
	ctx context.Context,
	sessionID uuid.UUID,
	jobID uuid.UUID,
	options ListJobSignalRequestsOptions,
) (*JobSignalRequestPage, error) {
	page, err := normalizeReadPage(options.Limit, options.Offset)
	if err != nil {
		return nil, err
	}

	job, err := s.GetJob(ctx, sessionID, jobID)
	if err != nil {
		return nil, err
	}

	request := s.query.JobSignalRequest
	query := request.WithContext(ctx).
		Where(
			request.SessionID.Eq(sessionID),
			request.JobID.Eq(jobID),
		).
		Order(request.RequestedAt.Asc(), request.ID.Asc())

	items, hasMore, err := listReadPage(
		page,
		func(offset, limit int) ([]*models.JobSignalRequest, error) {
			return query.Offset(offset).Limit(limit).Find()
		},
		"list job signal requests",
		"probe job signal request page",
	)
	if err != nil {
		return nil, err
	}

	return &JobSignalRequestPage{
		Job:     job,
		Items:   items,
		Limit:   page.limit,
		Offset:  page.offset,
		HasMore: hasMore,
	}, nil
}

// RecoverInterruptedJobs marks process rows left running by a prior process
// terminal. The original child process cannot be safely reattached.
func (s *Store) RecoverInterruptedJobs(ctx context.Context) (int, error) {
	var recovered int

	if err := s.query.Transaction(func(tx *repositories.Query) error {
		job := tx.Job

		running, err := job.WithContext(ctx).
			Where(job.State.Eq(string(models.JobStateRunning))).
			Find()
		if err != nil {
			return ctxerrors.Wrap(err, "list running jobs")
		}

		for _, item := range running {
			updated, updateErr := job.WithContext(ctx).
				Where(
					job.ID.Eq(item.ID),
					job.State.Eq(string(models.JobStateRunning)),
				).
				UpdateSimple(
					job.State.Value(string(models.JobStateInterrupted)),
					job.FailureDetail.Value(interruptedJobFailureDetail),
					job.EndedAt.Value(s.now()),
				)
			if updateErr != nil {
				return ctxerrors.Wrap(updateErr, "mark job interrupted")
			}

			if updated.RowsAffected == 1 {
				recovered++
			}
		}

		return nil
	}); err != nil {
		return 0, ctxerrors.Wrap(err, "recover interrupted jobs")
	}

	return recovered, nil
}

func validateCreateJobInput(sessionID uuid.UUID, input CreateJobInput) error {
	if sessionID == uuid.Nil || input.TurnID == uuid.Nil || input.PID <= 0 ||
		strings.TrimSpace(input.Purpose) == "" ||
		strings.TrimSpace(input.Command) == "" ||
		strings.TrimSpace(input.Directory) == "" {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "job")
	}

	return nil
}

func validateJobOutputInput(
	sessionID uuid.UUID,
	jobID uuid.UUID,
	input AppendJobOutputInput,
) error {
	if sessionID == uuid.Nil || jobID == uuid.Nil {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "job output")
	}

	if input.Stream != models.JobOutputStreamStdout &&
		input.Stream != models.JobOutputStreamStderr {
		return ctxerrors.Wrap(commerr.ErrValidationFailed, "job output stream")
	}

	return nil
}

func validateJobSignalInput(
	sessionID uuid.UUID,
	jobID uuid.UUID,
	input RecordJobSignalInput,
) error {
	if sessionID == uuid.Nil || jobID == uuid.Nil {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "job signal")
	}

	if input.Signal != models.JobSignalStop &&
		input.Signal != models.JobSignalKill {
		return ctxerrors.Wrap(commerr.ErrValidationFailed, "job signal")
	}

	return nil
}

func isJobTerminalState(state models.JobState) bool {
	return state == models.JobStateExited ||
		state == models.JobStateSignalled ||
		state == models.JobStateFailed ||
		state == models.JobStateInterrupted
}

func isJobState(state models.JobState) bool {
	return state == models.JobStateRunning || isJobTerminalState(state)
}

func normalizeJobListOptions(options ListJobsOptions) (ListJobsOptions, error) {
	if options.Limit == 0 {
		options.Limit = DefaultPageLimit
	}

	if options.Limit < 1 || options.Limit > MaximumPageLimit ||
		options.Offset < 0 {
		return ListJobsOptions{}, ctxerrors.Wrap(ErrInvalidPage, "job page")
	}

	if options.State != nil && !isJobState(*options.State) {
		return ListJobsOptions{}, ctxerrors.Wrap(ErrInvalidPage, "job state")
	}

	return options, nil
}

func normalizeJobOutputOptions(
	options ListJobOutputOptions,
) (ListJobOutputOptions, error) {
	if options.Limit == 0 {
		options.Limit = DefaultPageLimit
	}

	if options.Limit < 1 || options.Limit > MaximumPageLimit ||
		options.Cursor < 0 {
		return ListJobOutputOptions{}, ctxerrors.Wrap(
			ErrInvalidPage,
			"job output page",
		)
	}

	invalidStream := options.Stream != nil &&
		*options.Stream != models.JobOutputStreamStdout &&
		*options.Stream != models.JobOutputStreamStderr
	if invalidStream {
		return ListJobOutputOptions{}, ctxerrors.Wrap(
			ErrInvalidPage,
			"job output stream",
		)
	}

	return options, nil
}

func (s *Store) findJobWithQuery(
	ctx context.Context,
	query *repositories.Query,
	sessionID uuid.UUID,
	jobID uuid.UUID,
) (*models.Job, error) {
	job, err := query.Job.WithContext(ctx).
		Where(
			query.Job.ID.Eq(jobID),
			query.Job.SessionID.Eq(sessionID),
		).
		First()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "query job")
	}

	return job, nil
}
