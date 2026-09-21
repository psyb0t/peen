package agent

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The observer is the only thing that makes the live process registry and the
// SQLite record agree, so it is exercised against a real store rather than a
// stub. A stub would assert the call shape and miss the column mapping.
func TestDurableJobObserverRecordsTheWholeJobLifecycle(t *testing.T) {
	ctx := context.Background()
	fixture := newRuntimeFixture(
		t,
		elelemtest.NewScriptedDriver(elelemtest.Text("done")),
	)

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

	observer := durableJobObserver{store: fixture.store}
	started := time.Now().UTC()
	snapshot := tools.JobSnapshot{
		ID:                 uuid.New(),
		PID:                4242,
		SessionID:          turnResult.SessionID,
		TurnID:             turn.ID,
		WorkerGenerationID: uuid.New().String(),
		ToolCallID:         "call-observer",
		Purpose:            "watch the build",
		Command:            "make build",
		Directory:          fixture.workspace,
		State:              tools.JobStateRunning,
		StartedAt:          started,
	}

	require.NoError(t, observer.JobStarted(ctx, snapshot))

	for _, record := range []tools.JobOutputRecord{
		{Stream: tools.JobStreamStdout, Content: "compiling", CreatedAt: started},
		{Stream: tools.JobStreamStderr, Content: "a warning", CreatedAt: started},
	} {
		require.NoError(t, observer.JobOutput(ctx, snapshot, record))
	}

	require.NoError(t, observer.JobSignal(ctx, snapshot, tools.JobSignalStop))

	snapshot.State = tools.JobStateExited
	snapshot.ExitCode = 0
	snapshot.EndedAt = time.Now().UTC()
	require.NoError(t, observer.JobFinished(ctx, snapshot))

	jobs, err := fixture.runtime.ListSessionJobs(
		ctx,
		turnResult.SessionID,
		api.ListSessionJobsParams{},
	)
	require.NoError(t, err)
	require.Len(t, jobs.Jobs, 1)

	stored := jobs.Jobs[0]
	assert.Equal(t, snapshot.ID, stored.JobId)
	assert.Equal(t, api.JobState(models.JobStateExited), stored.State)
	assert.Equal(t, "make build", stored.Command)
	assert.Equal(t, "watch the build", stored.Purpose)
	assert.Equal(t, int64(4242), stored.Pid)
	assert.Equal(t, int64(0), stored.ExitCode)

	output, err := fixture.runtime.ReadSessionJobOutput(
		ctx,
		turnResult.SessionID,
		snapshot.ID,
		api.ReadSessionJobOutputParams{},
	)
	require.NoError(t, err)
	require.Len(t, output.Lines, 2)
	assert.Equal(t, "compiling", output.Lines[0].Content)
	assert.Equal(t, "a warning", output.Lines[1].Content)
}

// A signal sent to a job that is no longer running is recorded as not accepted,
// which is what lets a client tell "we asked and it was too late" from "we
// asked and it stopped".
func TestDurableJobObserverMarksASignalToAFinishedJobUnaccepted(t *testing.T) {
	ctx := context.Background()
	fixture := newRuntimeFixture(
		t,
		elelemtest.NewScriptedDriver(elelemtest.Text("done")),
	)

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

	observer := durableJobObserver{store: fixture.store}
	snapshot := tools.JobSnapshot{
		ID:                 uuid.New(),
		PID:                99,
		SessionID:          turnResult.SessionID,
		TurnID:             turn.ID,
		WorkerGenerationID: uuid.New().String(),
		ToolCallID:         "call-late-signal",
		Purpose:            "already gone",
		Command:            "true",
		Directory:          fixture.workspace,
		State:              tools.JobStateRunning,
		StartedAt:          time.Now().UTC(),
	}
	require.NoError(t, observer.JobStarted(ctx, snapshot))

	snapshot.State = tools.JobStateExited
	require.NoError(t, observer.JobSignal(ctx, snapshot, tools.JobSignalKill))

	signals, err := fixture.runtime.ListSessionJobSignalRequests(
		ctx,
		turnResult.SessionID,
		snapshot.ID,
		api.ListSessionJobSignalRequestsParams{},
	)
	require.NoError(t, err)
	require.Len(t, signals.SignalRequests, 1)
	assert.False(
		t,
		signals.SignalRequests[0].Accepted,
		"a signal to a job that already exited was not accepted",
	)
}

// Without a store the observer refuses rather than silently dropping the
// durable record, because a job the registry knows about and SQLite does not is
// exactly the divergence this type exists to prevent.
func TestDurableJobObserverRefusesWithoutAStore(t *testing.T) {
	t.Parallel()

	err := durableJobObserver{}.JobStarted(
		context.Background(),
		tools.JobSnapshot{ID: uuid.New()},
	)
	require.Error(t, err)
}

func TestJobEnumsMapToTheirDurableValues(t *testing.T) {
	t.Parallel()

	t.Run("state", func(t *testing.T) {
		t.Parallel()

		for state, want := range map[tools.JobState]models.JobState{
			tools.JobStateRunning:   models.JobStateRunning,
			tools.JobStateExited:    models.JobStateExited,
			tools.JobStateSignalled: models.JobStateSignalled,
			tools.JobStateFailed:    models.JobStateFailed,
		} {
			got, err := jobStateToModel(state)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		}

		_, err := jobStateToModel(tools.JobState("invented"))
		require.Error(t, err)
	})

	t.Run("output stream", func(t *testing.T) {
		t.Parallel()

		for stream, want := range map[tools.JobStream]models.JobOutputStream{
			tools.JobStreamStdout: models.JobOutputStreamStdout,
			tools.JobStreamStderr: models.JobOutputStreamStderr,
		} {
			got, err := jobOutputStreamToModel(stream)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		}

		// "both" is a read-side filter, never a stored stream, so it must be
		// rejected here rather than stored as a third stream value.
		_, err := jobOutputStreamToModel(tools.JobStreamBoth)
		require.Error(t, err)
	})

	t.Run("signal", func(t *testing.T) {
		t.Parallel()

		for signal, want := range map[tools.JobSignal]models.JobSignal{
			tools.JobSignalStop: models.JobSignalStop,
			tools.JobSignalKill: models.JobSignalKill,
		} {
			got, err := jobSignalToModel(signal)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		}

		_, err := jobSignalToModel(tools.JobSignal("hup"))
		require.Error(t, err)
	})
}

// An unmappable enum stops before SQLite is touched, so a bad value from the
// registry cannot write a row the read path would then fail to decode.
func TestDurableJobObserverRejectsUnmappableEnumsBeforeWriting(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	observer := durableJobObserver{store: nil}
	snapshot := tools.JobSnapshot{ID: uuid.New(), SessionID: uuid.New()}

	require.Error(t, observer.JobOutput(ctx, snapshot, tools.JobOutputRecord{
		Stream: tools.JobStream("sideband"),
	}))
	require.Error(t, observer.JobSignal(ctx, snapshot, tools.JobSignal("hup")))

	snapshot.State = tools.JobState("invented")
	require.Error(t, observer.JobFinished(ctx, snapshot))
}
