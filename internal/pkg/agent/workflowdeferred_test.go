package agent

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeferredSchedulingRetainsStoppedAndBusyInput(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		stopped bool
	}{
		{name: "later stop", stopped: true}, {name: "unrelated active turn"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, message, dispatch := deferredRootFixture(t)
			if tc.stopped {
				_, err := fixture.base.store.Cancel(t.Context(), message.SessionID)
				require.NoError(t, err)
			} else {
				lease, err := fixture.base.store.AcquireTurn(t.Context(), message.SessionID,
					session.StartTurnInput{RequestID: uuid.New(), Workspace: fixture.base.workspace})
				require.NoError(t, err)
				t.Cleanup(func() { fixture.base.store.ReleaseTurn(lease) })
			}
			native := &nativeWorkflowRunner{api: newWorkflowClient(fixture.storage), engine: fixture.engine, dispatch: dispatch}
			require.NoError(t, native.restoreWorkflowControls(t.Context()))
			row, err := fixture.base.store.GetExecutionMessageByRequest(t.Context(), message.SessionID, message.ExecutionID, message.RequestID)
			require.NoError(t, err)
			assert.Equal(t, models.InboxMessageStateDeferred, row.State)
			assert.Nil(t, row.ForwardedExecutionID)
			stats, err := fixture.storage.GetStats(t.Context())
			require.NoError(t, err)
			assert.Zero(t, stats.ActiveWorkflowInstances)
		})
	}
}
