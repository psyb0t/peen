package session

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/common-go/utils/ptrutil"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const jobTestCommand = "printf test"

func TestStoreJobReplaySurvivesRestartAndRecordsSignals(t *testing.T) {
	ctx := context.Background()
	stateDirectory := filepath.Join(t.TempDir(), "state")
	handle, err := db.Open(ctx, db.Config{Directory: stateDirectory})
	require.NoError(t, err)

	store := newTestStore(t, handle)
	session := newTestSession(ctx, t, store)
	lease := startAgentRunTestTurn(ctx, t, store, session.ID)
	jobs := make([]*models.Job, 0, 13)
	for index := range 13 {
		job := createJobForTest(ctx, t, store, session.ID, lease.TurnID, index)
		jobs = append(jobs, job)
	}

	for index, stream := range []models.JobOutputStream{
		models.JobOutputStreamStdout,
		models.JobOutputStreamStderr,
		models.JobOutputStreamStdout,
		models.JobOutputStreamStderr,
		models.JobOutputStreamStdout,
	} {
		line, appendErr := store.AppendJobOutput(
			ctx,
			session.ID,
			jobs[0].ID,
			AppendJobOutputInput{
				Stream:  stream,
				Content: fmt.Sprintf("line-%d", index),
			},
		)
		require.NoError(t, appendErr)
		assert.Equal(t, int64(index+1), line.Sequence)
		assert.Equal(t, stream, line.Stream)
	}

	accepted, err := store.RecordJobSignal(
		ctx,
		session.ID,
		jobs[0].ID,
		RecordJobSignalInput{
			Signal:   models.JobSignalStop,
			Accepted: true,
		},
	)
	require.NoError(t, err)
	assert.True(t, accepted.Accepted)
	assert.Equal(t, models.JobStateRunning, accepted.StateAtRequest)

	_, err = store.FinalizeJob(ctx, session.ID, jobs[0].ID, FinalizeJobInput{
		State:    models.JobStateExited,
		ExitCode: 0,
	})
	require.NoError(t, err)

	noOp, err := store.RecordJobSignal(
		ctx,
		session.ID,
		jobs[0].ID,
		RecordJobSignalInput{Signal: models.JobSignalKill},
	)
	require.NoError(t, err)
	assert.False(t, noOp.Accepted)
	assert.Equal(t, models.JobStateExited, noOp.StateAtRequest)

	var listed []uuid.UUID
	for offset := 0; ; offset += 5 {
		page, pageErr := store.ListJobs(ctx, session.ID, ListJobsOptions{
			Limit:  5,
			Offset: offset,
		})
		require.NoError(t, pageErr)
		for _, item := range page.Items {
			listed = append(listed, item.ID)
		}
		if !page.HasMore {
			break
		}
	}
	require.Len(t, listed, len(jobs))
	assert.ElementsMatch(t, jobIDs(jobs), listed)

	firstOutput, err := store.ListJobOutput(
		ctx,
		session.ID,
		jobs[0].ID,
		ListJobOutputOptions{Limit: 2},
	)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2}, jobOutputSequences(firstOutput.Items))
	assert.Equal(t, int64(2), firstOutput.NextCursor)
	assert.True(t, firstOutput.HasMore)
	assert.Equal(t, models.JobStateExited, firstOutput.Job.State)

	stdoutOutput, err := store.ListJobOutput(
		ctx,
		session.ID,
		jobs[0].ID,
		ListJobOutputOptions{
			Limit:  5,
			Stream: ptrutil.Of(models.JobOutputStreamStdout),
		},
	)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 3, 5}, jobOutputSequences(stdoutOutput.Items))
	assert.Equal(t, int64(5), stdoutOutput.NextCursor)
	assert.False(t, stdoutOutput.HasMore)

	signals, err := store.ListJobSignalRequests(
		ctx,
		session.ID,
		jobs[0].ID,
		ListJobSignalRequestsOptions{Limit: 5},
	)
	require.NoError(t, err)
	require.Len(t, signals.Items, 2)
	assert.True(t, signals.Items[0].Accepted)
	assert.False(t, signals.Items[1].Accepted)

	require.NoError(t, handle.Close())
	reopenedHandle, err := db.Open(ctx, db.Config{Directory: stateDirectory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopenedHandle.Close()) })
	reopenedStore := newTestStore(t, reopenedHandle)

	replayed, err := reopenedStore.ListJobOutput(
		ctx,
		session.ID,
		jobs[0].ID,
		ListJobOutputOptions{Cursor: 2, Limit: 5},
	)
	require.NoError(t, err)
	assert.Equal(t, []int64{3, 4, 5}, jobOutputSequences(replayed.Items))
	assert.Equal(t, int64(5), replayed.NextCursor)
	assert.False(t, replayed.HasMore)

	recovered, err := reopenedStore.RecoverInterruptedJobs(ctx)
	require.NoError(t, err)
	assert.Equal(t, len(jobs)-1, recovered)

	interrupted, err := reopenedStore.GetJob(ctx, session.ID, jobs[1].ID)
	require.NoError(t, err)
	assert.Equal(t, models.JobStateInterrupted, interrupted.State)
	assert.Equal(t, interruptedJobFailureDetail, interrupted.FailureDetail)

	secondRecovery, err := reopenedStore.RecoverInterruptedJobs(ctx)
	require.NoError(t, err)
	assert.Zero(t, secondRecovery)
}

func TestStoreJobSessionIsolation(t *testing.T) {
	ctx := context.Background()
	store, handle := openTestStore(t)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	firstSession := newTestSession(ctx, t, store)
	secondSession := newTestSession(ctx, t, store)
	lease := startAgentRunTestTurn(ctx, t, store, firstSession.ID)
	job := createJobForTest(ctx, t, store, firstSession.ID, lease.TurnID, 0)

	_, err := store.GetJob(ctx, secondSession.ID, job.ID)
	require.ErrorIs(t, err, commerr.ErrNotFound)

	_, err = store.ListJobOutput(
		ctx,
		secondSession.ID,
		job.ID,
		ListJobOutputOptions{Limit: 1},
	)
	require.ErrorIs(t, err, commerr.ErrNotFound)

	_, err = store.RecordJobSignal(
		ctx,
		secondSession.ID,
		job.ID,
		RecordJobSignalInput{Signal: models.JobSignalStop},
	)
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

func createJobForTest(
	ctx context.Context,
	t *testing.T,
	store *Store,
	sessionID uuid.UUID,
	turnID uuid.UUID,
	index int,
) *models.Job {
	t.Helper()

	job, err := store.CreateJob(ctx, sessionID, CreateJobInput{
		ID:        uuid.New(),
		TurnID:    turnID,
		PID:       int64(index + 1),
		Purpose:   fmt.Sprintf("job-%02d", index),
		Command:   jobTestCommand,
		Directory: testWorkspace,
	})
	require.NoError(t, err)

	return job
}

func jobOutputSequences(items []*models.JobOutputLine) []int64 {
	sequences := make([]int64, 0, len(items))
	for _, item := range items {
		sequences = append(sequences, item.Sequence)
	}

	return sequences
}

func jobIDs(items []*models.Job) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}

	return ids
}
