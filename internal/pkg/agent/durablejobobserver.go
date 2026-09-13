package agent

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/tools"
)

// durableJobObserver makes the live process registry an adapter over the
// session's SQLite record, rather than a second source of truth.
type durableJobObserver struct {
	store *session.Store
}

func (o durableJobObserver) JobStarted(
	ctx context.Context,
	snapshot tools.JobSnapshot,
) error {
	if o.store == nil {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "job store")
	}

	_, err := o.store.CreateJob(ctx, snapshot.SessionID, session.CreateJobInput{
		ID:         snapshot.ID,
		TurnID:     snapshot.TurnID,
		ToolCallID: snapshot.ToolCallID,
		PID:        int64(snapshot.PID),
		Purpose:    snapshot.Purpose,
		Command:    snapshot.Command,
		Directory:  snapshot.Directory,
		StartedAt:  snapshot.StartedAt,
	})
	if err != nil {
		return ctxerrors.Wrap(err, "create durable job")
	}

	return nil
}

func (o durableJobObserver) JobOutput(
	ctx context.Context,
	snapshot tools.JobSnapshot,
	record tools.JobOutputRecord,
) error {
	stream, err := jobOutputStreamToModel(record.Stream)
	if err != nil {
		return err
	}

	_, err = o.store.AppendJobOutput(
		ctx,
		snapshot.SessionID,
		snapshot.ID,
		session.AppendJobOutputInput{
			Stream:    stream,
			Content:   record.Content,
			CreatedAt: record.CreatedAt,
		},
	)
	if err != nil {
		return ctxerrors.Wrap(err, "append durable job output")
	}

	return nil
}

func (o durableJobObserver) JobSignal(
	ctx context.Context,
	snapshot tools.JobSnapshot,
	signal tools.JobSignal,
) error {
	modelSignal, err := jobSignalToModel(signal)
	if err != nil {
		return err
	}

	_, err = o.store.RecordJobSignal(
		ctx,
		snapshot.SessionID,
		snapshot.ID,
		session.RecordJobSignalInput{
			Signal:   modelSignal,
			Accepted: snapshot.State == tools.JobStateRunning,
		},
	)
	if err != nil {
		return ctxerrors.Wrap(err, "record durable job signal")
	}

	return nil
}

func (o durableJobObserver) JobFinished(
	ctx context.Context,
	snapshot tools.JobSnapshot,
) error {
	state, err := jobStateToModel(snapshot.State)
	if err != nil {
		return err
	}

	_, err = o.store.FinalizeJob(
		ctx,
		snapshot.SessionID,
		snapshot.ID,
		session.FinalizeJobInput{
			State:         state,
			ExitCode:      int64(snapshot.ExitCode),
			FailureDetail: snapshot.FailureDetail,
		},
	)
	if err != nil {
		return ctxerrors.Wrap(err, "finalize durable job")
	}

	return nil
}

func jobStateToModel(state tools.JobState) (models.JobState, error) {
	switch state {
	case tools.JobStateRunning:
		return models.JobStateRunning, nil
	case tools.JobStateExited:
		return models.JobStateExited, nil
	case tools.JobStateSignalled:
		return models.JobStateSignalled, nil
	case tools.JobStateFailed:
		return models.JobStateFailed, nil
	default:
		return "", ctxerrors.Wrap(commerr.ErrValidationFailed, "job state")
	}
}

func jobOutputStreamToModel(
	stream tools.JobStream,
) (models.JobOutputStream, error) {
	switch stream {
	case tools.JobStreamStdout:
		return models.JobOutputStreamStdout, nil
	case tools.JobStreamStderr:
		return models.JobOutputStreamStderr, nil
	default:
		return "", ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"job output stream",
		)
	}
}

func jobSignalToModel(signal tools.JobSignal) (models.JobSignal, error) {
	switch signal {
	case tools.JobSignalStop:
		return models.JobSignalStop, nil
	case tools.JobSignalKill:
		return models.JobSignalKill, nil
	default:
		return "", ctxerrors.Wrap(commerr.ErrValidationFailed, "job signal")
	}
}
