package agent

import (
	"testing"

	"github.com/cschleiden/go-workflows/backend"
	"github.com/cschleiden/go-workflows/client"
	"github.com/cschleiden/go-workflows/core"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestRootSubmissionFailureReleasesOnlyProvenMissingWorkflow(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		lookupErr error
		released  bool
		uncached  bool
	}{
		{name: "missing workflow", lookupErr: backend.ErrInstanceNotFound, released: true},
		{name: "missing callback cache", lookupErr: backend.ErrInstanceNotFound, released: true, uncached: true},
		{name: "submitted with lost acknowledgment"},
		{name: "unknown submission state", lookupErr: commerr.ErrFetchFailed},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, prepared, dispatch := newRootSubmissionFixture(t)
			input, err := dispatch.prepareRootSubmission(t.Context(), prepared)
			require.NoError(t, err)
			if tc.uncached {
				dispatch.root.slot(input.ExecutionID).current = nil
			}
			sdk := &backend.MockBackend{}
			t.Cleanup(func() { sdk.AssertExpectations(t) })
			sdk.On("GetWorkflowInstanceState", mock.Anything, nativeWorkflowInstance(input.SessionID, input.ExecutionID)).Return(core.WorkflowInstanceStateActive, tc.lookupErr).Once()
			native := &nativeWorkflowRunner{api: client.New(sdk), engine: fixture.engine, dispatch: dispatch}
			err = native.cleanupFailedSubmission(t.Context(), prepared, input)
			if tc.lookupErr != nil && !tc.released {
				require.ErrorIs(t, err, tc.lookupErr)
			} else {
				require.NoError(t, err)
			}
			lease, err := fixture.base.store.AcquireTurn(t.Context(), input.SessionID, session.StartTurnInput{RequestID: uuid.New(), Workspace: fixture.base.workspace})
			if tc.released {
				require.NoError(t, err)
				fixture.base.store.ReleaseTurn(lease)
				assert.Nil(t, dispatch.root.slot(input.ExecutionID).current)
			} else {
				require.ErrorIs(t, err, session.ErrSessionBusy)
				assert.NotNil(t, dispatch.root.slot(input.ExecutionID).current)
			}
			saved, err := fixture.base.store.GetExecution(t.Context(), input.SessionID, input.ExecutionID)
			require.NoError(t, err)
			assert.Zero(t, saved.ControlRevision)
		})
	}
}
