package db

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/go-workflows/backend"
	"github.com/psyb0t/go-workflows/backend/history"
	"github.com/psyb0t/go-workflows/core"
	"github.com/psyb0t/go-workflows/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type workflowResolutionFixture struct {
	backend.Backend
	remaining        int
	reads            int
	events           []*history.Event
	failure          error
	historyFailure   error
	successorFailure error
}

func (b *workflowResolutionFixture) GetWorkflowInstanceState(
	context.Context, *workflow.Instance,
) (core.WorkflowInstanceState, error) {
	b.reads++
	if b.reads > 1 && b.successorFailure != nil {
		return core.WorkflowInstanceStateActive, b.successorFailure
	}

	if b.remaining == 0 {
		return core.WorkflowInstanceStateActive, b.failure
	}

	b.remaining--

	return core.WorkflowInstanceStateContinuedAsNew, b.failure
}

func (b *workflowResolutionFixture) GetWorkflowInstanceHistory(
	context.Context, *workflow.Instance, *int64,
) ([]*history.Event, error) {
	return b.events, b.historyFailure
}

func TestResolveWorkflowExecutionBoundsAndValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name             string
		handoffs         int
		successor        string
		failure          error
		wantErr          error
		missingHistory   bool
		historyFailure   error
		successorFailure error
	}{
		{name: "unchanged"},
		{name: "one", handoffs: 1, successor: uuid.NewString()},
		{
			name: "exact bound", handoffs: WorkflowContinuationLimit,
			successor: uuid.NewString(),
		},
		{
			name: "over bound", handoffs: WorkflowContinuationLimit + 1,
			successor: uuid.NewString(), wantErr: commerr.ErrInvalidState,
		},
		{
			name: "missing successor", handoffs: 1,
			wantErr: commerr.ErrInvalidState,
		},
		{
			name: "malformed successor", handoffs: 1,
			successor: "invalid", wantErr: commerr.ErrInvalidState,
		},
		{
			name: "read failure", failure: commerr.ErrFetchFailed,
			wantErr: commerr.ErrFetchFailed,
		},
		{
			name: "missing continuation history", handoffs: 1,
			missingHistory: true, wantErr: commerr.ErrInvalidState,
		},
		{
			name: "history read failure", handoffs: 1,
			historyFailure: commerr.ErrFetchFailed,
			wantErr:        commerr.ErrFetchFailed,
		},
		{
			name: "missing successor execution", handoffs: 1,
			successor:        uuid.NewString(),
			successorFailure: backend.ErrInstanceNotFound,
			wantErr:          commerr.ErrInvalidState,
		},
		{
			name: "successor read failure", handoffs: 1,
			successor:        uuid.NewString(),
			successorFailure: commerr.ErrFetchFailed,
			wantErr:          commerr.ErrFetchFailed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			instance := &workflow.Instance{
				InstanceID:  uuid.NewString(),
				ExecutionID: uuid.NewString(),
			}
			storage := &workflowResolutionFixture{
				remaining:        tc.handoffs,
				historyFailure:   tc.historyFailure,
				successorFailure: tc.successorFailure,
				failure:          tc.failure, events: []*history.Event{{
					Type: history.EventType_WorkflowExecutionContinuedAsNew,
					Attributes: &history.ExecutionContinuedAsNewAttributes{
						ContinuedExecutionID: tc.successor,
					},
				}},
			}
			if tc.missingHistory {
				storage.events = nil
			}
			resolved, err := ResolveWorkflowExecution(t.Context(), storage,
				instance)
			require.ErrorIs(t, err, tc.wantErr)
			assert.LessOrEqual(t, storage.reads, WorkflowContinuationLimit+1)
			if err != nil {
				assert.Nil(t, resolved)

				return
			}

			assert.Equal(t, instance.InstanceID, resolved.InstanceID)
			if tc.handoffs == 0 {
				assert.Equal(t, instance.ExecutionID, resolved.ExecutionID)
			} else {
				assert.Equal(t, tc.successor, resolved.ExecutionID)
			}
		})
	}
}
