package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChildSubmissionAcceptsOnlyOwnedHandlerPhase(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		phase    workflowToolPhase
		previous workflowToolPhase
		tool     string
		wantErr  error
	}{
		{
			name: "handler", phase: workflowToolPhaseHandler,
			previous: workflowToolPhasePre, tool: toolNameLaunchAgent,
		},
		{
			name: "pre-hook", phase: workflowToolPhasePre, tool: toolNameLaunchAgent,
			wantErr: commerr.ErrConflict,
		},
		{
			name: "outcome hook", phase: workflowToolPhaseOutcome,
			previous: workflowToolPhaseHandler, tool: toolNameLaunchAgent,
			wantErr: commerr.ErrConflict,
		},
		{
			name: "changed predecessor", phase: workflowToolPhaseHandler,
			previous: workflowToolPhasePost, tool: toolNameLaunchAgent,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "another tool", phase: workflowToolPhaseHandler,
			previous: workflowToolPhasePre, tool: toolNameReadFile,
			wantErr: commerr.ErrConflict,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, native, deps, ctx, _ := nativeChildParentFixture(t)
			input := workflowToolPhaseInput{
				Tool: workflowToolInput{
					Round: workflowRoundInput{Execution: fixture.input},
					Call: elelem.ToolCall{
						ID: parentToolCallIDFromContext(ctx), Name: tc.tool,
					},
				},
				Phase: tc.phase,
				State: workflowToolPhaseState{LastPhase: tc.previous},
			}
			encoded, err := json.Marshal(input)
			require.NoError(t, err)
			claim, err := fixture.base.store.BeginExecutionActivity(ctx,
				fixture.input.SessionID, fixture.input.ExecutionID,
				fixture.engine.options.GenerationID, session.ScheduleActivityInput{
					StepID: "phaseParent", Kind: models.ActivityKindTool,
					Name:         toolPhaseActivityName,
					ReplayPolicy: models.ActivityReplayPolicyReconcile, Input: encoded,
				})
			require.NoError(t, err)
			ctx = context.WithValue(ctx, workflowActivityLeaseContextKey{},
				claim.Lease)
			parent, err := native.childSubmissionParent(ctx, deps)
			require.ErrorIs(t, err, tc.wantErr)
			if tc.wantErr == nil {
				require.NotNil(t, parent)
				assert.Equal(t, fixture.input.ExecutionID, parent.ID)
			}
		})
	}
}
