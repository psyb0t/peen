package agent

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootCallbackPauseReleasesOnlyPausedHolder(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		resume  bool
		foreign bool
		wantErr error
	}{
		{name: "paused"},
		{name: "resumed", resume: true, wantErr: commerr.ErrConflict},
		{name: "foreign holder", foreign: true, wantErr: commerr.ErrConflict},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, callbacks, shared := newRootCallbackFixture(t,
				elelemtest.NewScriptedDriver())
			_, err := fixture.base.store.PauseExecution(t.Context(),
				fixture.input.SessionID, fixture.input.ExecutionID, "retain work")
			require.NoError(t, err)
			if tc.resume {
				_, err = fixture.base.store.ResumeExecution(t.Context(),
					fixture.input.SessionID, fixture.input.ExecutionID)
				require.NoError(t, err)
			}
			if tc.foreign {
				callbacks.generationID = uuid.New()
			}

			execution, err := fixture.base.store.GetExecution(t.Context(),
				fixture.input.SessionID, fixture.input.ExecutionID)
			require.NoError(t, err)
			err = callbacks.retirePaused(t.Context(), fixture.base.store,
				execution)
			require.ErrorIs(t, err, tc.wantErr)
			assert.Equal(t, tc.wantErr != nil,
				fixture.base.store.IsActive(fixture.input.SessionID))
			if tc.wantErr != nil {
				assert.Same(t, shared, callbacks.slot(execution.ID).current)

				return
			}

			assert.Nil(t, callbacks.slot(execution.ID).current)
			require.NoError(t, callbacks.retirePaused(t.Context(),
				fixture.base.store, execution))
		})
	}
}

func TestRootCallbackPausePreservesPreviousGenerationHolder(t *testing.T) {
	t.Parallel()
	fixture, callbacks, _ := newRootCallbackFixture(t,
		elelemtest.NewScriptedDriver())
	_, err := fixture.base.store.PauseExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID, "retain work")
	require.NoError(t, err)
	execution, err := fixture.base.store.GetExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	holder, err := fixture.base.store.GetExecutionTurnLease(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	callbacks.generationID = uuid.New()
	callbacks.slots = nil

	require.NoError(t, callbacks.retirePaused(t.Context(), fixture.base.store,
		execution))
	retained, err := fixture.base.store.GetExecutionTurnLease(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, holder, retained)
	assert.True(t, fixture.base.store.IsActive(fixture.input.SessionID))
}
