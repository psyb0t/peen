package supervisor_test

import (
	"context"
	"testing"
	"time"

	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/psyb0t/peen/internal/pkg/worker/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
