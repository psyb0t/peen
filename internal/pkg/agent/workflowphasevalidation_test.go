package agent

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func phaseValidationLease(ctx context.Context, t *testing.T) session.ActivityLease {
	t.Helper()
	lease, ok := ctx.Value(workflowActivityLeaseContextKey{}).(session.ActivityLease)
	require.True(t, ok)

	return lease
}

func TestPhaseValidationChecksRecordedEnvelopeAndLease(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		change  func(*testing.T, context.Context, *workflowToolPhaseInput) context.Context
		wantErr error
	}{
		{name: "current", change: func(_ *testing.T, ctx context.Context,
			_ *workflowToolPhaseInput,
		) context.Context {
			return ctx
		}},
		{name: "missing lease", change: func(_ *testing.T, ctx context.Context,
			_ *workflowToolPhaseInput,
		) context.Context {
			return context.WithValue(ctx, workflowActivityLeaseContextKey{}, "missing")
		}, wantErr: commerr.ErrRequiredFieldNotSet},
		{name: "wrong generation", change: func(t *testing.T, ctx context.Context,
			_ *workflowToolPhaseInput,
		) context.Context {
			t.Helper()
			lease := phaseValidationLease(ctx, t)
			lease.GenerationID = uuid.New()

			return context.WithValue(ctx, workflowActivityLeaseContextKey{}, lease)
		}, wantErr: commerr.ErrConflict},
		{name: "wrong attempt", change: func(t *testing.T, ctx context.Context,
			_ *workflowToolPhaseInput,
		) context.Context {
			t.Helper()
			lease := phaseValidationLease(ctx, t)
			lease.Attempt++

			return context.WithValue(ctx, workflowActivityLeaseContextKey{}, lease)
		}, wantErr: commerr.ErrConflict},
		{name: "wrong activity", change: func(t *testing.T, ctx context.Context,
			_ *workflowToolPhaseInput,
		) context.Context {
			t.Helper()
			lease := phaseValidationLease(ctx, t)
			lease.ID = uuid.New()

			return context.WithValue(ctx, workflowActivityLeaseContextKey{}, lease)
		}, wantErr: commerr.ErrNotFound},
		{name: "changed call", change: func(_ *testing.T, ctx context.Context,
			input *workflowToolPhaseInput,
		) context.Context {
			input.Tool.Call.ID = "changedCall"

			return ctx
		}, wantErr: commerr.ErrConflict},
		{name: "changed phase", change: func(_ *testing.T, ctx context.Context,
			input *workflowToolPhaseInput,
		) context.Context {
			input.Phase = workflowToolPhaseHandler
			input.State.LastPhase = workflowToolPhasePre

			return ctx
		}, wantErr: commerr.ErrConflict},
		{name: "foreign session", change: func(_ *testing.T, ctx context.Context,
			input *workflowToolPhaseInput,
		) context.Context {
			input.Tool.Round.Execution.SessionID = uuid.New()

			return ctx
		}, wantErr: commerr.ErrNotFound},
		{name: "foreign execution", change: func(_ *testing.T, ctx context.Context,
			input *workflowToolPhaseInput,
		) context.Context {
			input.Tool.Round.Execution.ExecutionID = uuid.New()

			return ctx
		}, wantErr: commerr.ErrConflict},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionLoopFixture(t,
				elelemtest.NewScriptedDriver(), true)
			var validationErr error
			fixture.engine.options.ToolPhase = func(ctx context.Context,
				input workflowToolPhaseInput,
			) (workflowToolPhaseState, error) {
				ctx = tc.change(t, ctx, &input)
				_, validationErr = fixture.base.runtime.validateWorkflowPhaseActivity(
					ctx, input, fixture.engine.options.GenerationID)

				return input.State, validationErr
			}
			result, err := fixture.engine.toolPhase(t.Context(),
				toolPhaseTestInput(fixture))
			require.NoError(t, err)
			require.ErrorIs(t, validationErr, tc.wantErr)
			want := session.ActivityDecisionExecute
			if tc.wantErr != nil {
				want = session.ActivityDecisionReconcile
			}

			assert.Equal(t, want, result.Decision)
		})
	}
}
