package supervisor_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/psyb0t/peen/internal/pkg/worker/protocol"
	"github.com/psyb0t/peen/internal/pkg/worker/supervisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	recoveryTestWorkflowVersion = "testExecutionV1"
	recoveryTestTask            = "inspect example files"
	recoveryTestContext         = `{"messages":[]}`
	recoveryTestStepID          = "model:0"
	recoveryTestActivityName    = "modelRound"
	recoveryTestActivityInput   = `{}`
)

func TestDisconnectedWorkerIsRecordedAndReplaced(t *testing.T) {
	f := newFixture(t, true)
	first := f.ensure(t)
	first.Disconnect()
	require.Eventually(t, func() bool {
		generation, err := f.store.GetWorkerGeneration(t.Context(), f.sessionID, first.GenerationID)

		return err == nil && generation.State == string(worker.StateFailed)
	}, testEventuallyTimeout, time.Millisecond)
	second := f.ensure(t)
	assert.NotEqual(t, first.GenerationID, second.GenerationID)
	f.supervisor.WorkerDisconnected(context.Background(), f.sessionID, first.GenerationID)
	result, err := second.RunTurn(t.Context(), protocol.RunTurn{Message: "still connected"})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)
	current, found := f.supervisor.Current(f.sessionID)
	require.True(t, found)
	assert.Equal(t, second.GenerationID, current.GenerationID)
}

// startRecoveryTestExecution records a root execution with a running activity
// on the given worker generation, as a live turn leaves it.
func startRecoveryTestExecution(
	t *testing.T,
	f fixture,
	generationID uuid.UUID,
) uuid.UUID {
	t.Helper()
	lease, err := f.store.AcquireTurn(t.Context(), f.sessionID,
		session.StartTurnInput{RequestID: uuid.New(), Workspace: f.workspace})
	require.NoError(t, err)
	t.Cleanup(func() { f.store.ReleaseTurn(lease) })
	execution, err := f.store.CreateExecution(t.Context(), f.sessionID,
		session.CreateExecutionInput{
			ID: uuid.New(), RootTurnID: lease.TurnID,
			WorkflowVersion: recoveryTestWorkflowVersion,
			Task:            recoveryTestTask, WorkingDirectory: f.workspace,
			Context: json.RawMessage(recoveryTestContext),
		})
	require.NoError(t, err)
	_, err = f.store.BeginExecutionActivity(t.Context(), f.sessionID,
		execution.ID, generationID, session.ScheduleActivityInput{
			StepID: recoveryTestStepID, Kind: models.ActivityKindModel,
			ReplayPolicy: models.ActivityReplayPolicyRetry,
			Name:         recoveryTestActivityName,
			Input:        json.RawMessage(recoveryTestActivityInput),
		})
	require.NoError(t, err)

	return execution.ID
}

func TestDisconnectedWorkerWorkFollowsCrashRecovery(t *testing.T) {
	testCases := []struct {
		name             string
		resumeAfterCrash bool
		want             models.ExecutionState
	}{
		{name: "pause", want: models.ExecutionStatePaused},
		{
			name:             "resume",
			resumeAfterCrash: true,
			want:             models.ExecutionStateRunning,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, true, func(options *supervisor.Options) {
				options.ResumeAfterCrash = tc.resumeAfterCrash
			})
			first := f.ensure(t)
			executionID := startRecoveryTestExecution(t, f, first.GenerationID)
			first.Disconnect()
			require.Eventually(t, func() bool {
				generation, err := f.store.GetWorkerGeneration(t.Context(),
					f.sessionID, first.GenerationID)

				return err == nil &&
					generation.State == string(worker.StateFailed)
			}, testEventuallyTimeout, time.Millisecond)
			stored, err := f.store.GetExecution(t.Context(), f.sessionID,
				executionID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, stored.State)
		})
	}
}
