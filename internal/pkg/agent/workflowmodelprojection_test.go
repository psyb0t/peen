package agent

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/stretchr/testify/require"
)

func TestCommittedModelProjectionChecksExactSavedIdentity(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name            string
		changedInput    bool
		changedOutput   bool
		missingActivity bool
		wantErr         error
	}{
		{name: "unchanged"},
		{name: "input", changedInput: true, wantErr: commerr.ErrConflict},
		{name: "output", changedOutput: true, wantErr: commerr.ErrConflict},
		{name: "missing", missingActivity: true, wantErr: commerr.ErrNotFound},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, _, _ := rootModelProjectionFixture(t,
				elelemtest.Text("saved model result"))
			input := workflowRoundInput{Execution: fixture.input}
			result, err := fixture.engine.modelRound(t.Context(), input)
			require.NoError(t, err)
			activityID := result.Output.RootProjection.ActivityID
			if tc.changedInput {
				input.Round++
			}
			if tc.changedOutput {
				result.Output.Text = "changed result"
			}
			if tc.missingActivity {
				activityID = uuid.New()
			}
			err = validateCommittedModelRound(t.Context(), fixture.base.runtime,
				input, result.Output, activityID)
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}
