package agent

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExactTurnInterruptionLeavesAnotherRootRunning(t *testing.T) {
	t.Parallel()
	fixture, started, stopped := coldNativeStopFixture(t)
	select {
	case <-started:
	case <-time.After(workflowTestResultTimeout):
		t.Fatal("native tool did not start")
	}
	runtime := fixture.base.runtime
	require.NoError(t, runtime.InterruptTurn(t.Context(), uuid.New()))
	select {
	case <-stopped:
		t.Fatal("interruption reached another root")
	default:
	}
	execution, err := fixture.base.store.GetExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateRunning, execution.State)
	turn, err := fixture.base.store.GetExecutionRootTurn(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	turnID, err := fixture.base.store.CancelRequest(t.Context(),
		fixture.input.SessionID, turn.RequestID)
	require.NoError(t, err)
	assert.Equal(t, execution.RootTurnID, turnID)
	require.NoError(t, runtime.InterruptTurn(t.Context(), execution.RootTurnID))
	select {
	case <-stopped:
	case <-time.After(workflowTestResultTimeout):
		t.Fatal("interrupted root tool did not stop")
	}
	execution, err = fixture.base.store.GetExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, execution.State)
	require.ErrorIs(t, runtime.InterruptTurn(t.Context(), uuid.Nil),
		commerr.ErrValidationFailed)
}
