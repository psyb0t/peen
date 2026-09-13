package agent

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeReplaysJobsFromSQLiteWithoutALiveRegistry(t *testing.T) {
	ctx := context.Background()
	pageLimit := int32(2)
	streamPageLimit := int32(5)
	stdoutStream := api.ReadSessionJobOutputParamsStreamStdout
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(elelemtest.Text("done")))
	turnResult, err := fixture.runtime.Run(ctx, TurnRequest{
		Message:   "open the session",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	query := repositories.Use(fixture.handle.GormDB)
	turn, err := query.Turn.WithContext(ctx).
		Where(query.Turn.SessionID.Eq(turnResult.SessionID)).
		First()
	require.NoError(t, err)

	job, err := fixture.store.CreateJob(ctx, turnResult.SessionID, session.CreateJobInput{
		ID:         uuid.New(),
		TurnID:     turn.ID,
		ToolCallID: "call-1",
		PID:        321,
		Purpose:    "inspect durable replay",
		Command:    "printf durable",
		Directory:  fixture.workspace,
	})
	require.NoError(t, err)

	for _, input := range []session.AppendJobOutputInput{
		{Stream: models.JobOutputStreamStdout, Content: "first"},
		{Stream: models.JobOutputStreamStderr, Content: "problem"},
		{Stream: models.JobOutputStreamStdout, Content: "last"},
	} {
		_, appendErr := fixture.store.AppendJobOutput(ctx, turnResult.SessionID, job.ID, input)
		require.NoError(t, appendErr)
	}
	_, err = fixture.store.RecordJobSignal(
		ctx,
		turnResult.SessionID,
		job.ID,
		session.RecordJobSignalInput{
			Signal:   models.JobSignalStop,
			Accepted: true,
		},
	)
	require.NoError(t, err)
	_, err = fixture.store.FinalizeJob(
		ctx,
		turnResult.SessionID,
		job.ID,
		session.FinalizeJobInput{State: models.JobStateExited, ExitCode: 0},
	)
	require.NoError(t, err)

	jobs, err := fixture.runtime.ListSessionJobs(
		ctx,
		turnResult.SessionID,
		api.ListSessionJobsParams{},
	)
	require.NoError(t, err)
	require.Len(t, jobs.Jobs, 1)
	assert.Equal(t, job.ID, jobs.Jobs[0].JobId)
	assert.Equal(t, turn.ID, jobs.Jobs[0].TurnId)
	assert.Equal(t, turnResult.SessionID, jobs.Jobs[0].SessionId)
	assert.Equal(t, int64(321), jobs.Jobs[0].Pid)
	assert.Equal(t, api.JobStateExited, jobs.Jobs[0].State)
	require.NotNil(t, jobs.Jobs[0].ToolCallId)
	assert.Equal(t, "call-1", *jobs.Jobs[0].ToolCallId)

	output, err := fixture.runtime.ReadSessionJobOutput(
		ctx,
		turnResult.SessionID,
		job.ID,
		api.ReadSessionJobOutputParams{Limit: &pageLimit},
	)
	require.NoError(t, err)
	assert.Equal(t, api.JobOutputStateExited, output.State)
	assert.Equal(t, int64(2), output.NextCursor)
	assert.True(t, output.HasMore)
	assert.Equal(t, []int64{1, 2}, outputLineSequences(output.Lines))
	assert.Equal(
		t,
		[]api.JobOutputLineStream{
			api.JobOutputLineStreamStdout,
			api.JobOutputLineStreamStderr,
		},
		outputLineStreams(output.Lines),
	)

	stdout, err := fixture.runtime.ReadSessionJobOutput(
		ctx,
		turnResult.SessionID,
		job.ID,
		api.ReadSessionJobOutputParams{
			Limit:  &streamPageLimit,
			Stream: &stdoutStream,
		},
	)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 3}, outputLineSequences(stdout.Lines))
	assert.Equal(t, int64(3), stdout.NextCursor)
	assert.False(t, stdout.HasMore)

	signals, err := fixture.runtime.ListSessionJobSignalRequests(
		ctx,
		turnResult.SessionID,
		job.ID,
		api.ListSessionJobSignalRequestsParams{},
	)
	require.NoError(t, err)
	require.Len(t, signals.SignalRequests, 1)
	assert.True(t, signals.SignalRequests[0].Accepted)
	assert.Equal(t, api.JobSignalRecordSignalStop, signals.SignalRequests[0].Signal)
	assert.Equal(
		t,
		api.JobSignalRecordStateAtRequestRunning,
		signals.SignalRequests[0].StateAtRequest,
	)

	signalResponse, err := fixture.runtime.SignalSessionJob(
		ctx,
		turnResult.SessionID,
		job.ID,
		api.JobSignalRequest{Signal: api.JobSignalRequestSignalKill},
	)
	require.NoError(t, err)
	assert.False(t, signalResponse.Signalled)
	assert.Equal(t, api.JobSignalResponseStateExited, signalResponse.State)

	signals, err = fixture.runtime.ListSessionJobSignalRequests(
		ctx,
		turnResult.SessionID,
		job.ID,
		api.ListSessionJobSignalRequestsParams{},
	)
	require.NoError(t, err)
	require.Len(t, signals.SignalRequests, 2)
	assert.False(t, signals.SignalRequests[1].Accepted)
	assert.Equal(t, api.JobSignalRecordSignalKill, signals.SignalRequests[1].Signal)
}

func outputLineSequences(lines []api.JobOutputLine) []int64 {
	result := make([]int64, 0, len(lines))
	for _, line := range lines {
		result = append(result, line.Sequence)
	}

	return result
}

func outputLineStreams(lines []api.JobOutputLine) []api.JobOutputLineStream {
	result := make([]api.JobOutputLineStream, 0, len(lines))
	for _, line := range lines {
		result = append(result, line.Stream)
	}

	return result
}
