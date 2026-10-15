package agent

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/essessey"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolStreamOffsetsRetainParallelAndAttemptSlots(t *testing.T) {
	t.Parallel()
	stream := &workflowToolStream{Base: 3, Count: 2}
	first, err := stream.offset(0, 1)
	require.NoError(t, err)
	second, err := stream.offset(1, 1)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	retry, err := stream.offset(0, 2)
	require.NoError(t, err)
	assert.Equal(t, 7, retry)
}

func TestToolStreamBootstrapRejectsMismatchedBatch(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(workflowConversation{
		Stream: &workflowToolStream{Base: 1, Count: 2},
	})
	require.NoError(t, err)
	ctx := context.WithValue(t.Context(), workflowActivityLeaseContextKey{},
		session.ActivityLease{Attempt: 1})
	prepared := &preparedTurn{}
	_, err = prepared.workflowToolAdapter(ctx, workflowToolInput{
		Round: workflowRoundInput{Execution: workflowExecutionInput{
			Context: encoded,
		}},
	})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

func TestToolStreamBootstrapRequiresActivityLease(t *testing.T) {
	t.Parallel()
	prepared := &preparedTurn{}
	_, err := prepared.workflowToolAdapter(t.Context(), workflowToolInput{
		Round: workflowRoundInput{Execution: workflowExecutionInput{
			Context: json.RawMessage(`{}`),
		}},
	})
	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
}

func TestToolStreamBootstrapEmitsNothingUntilToolStarts(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(workflowConversation{
		Stream: &workflowToolStream{Base: 9, Count: 1},
		Messages: []elelem.Message{{
			Role:      elelem.RoleAssistant,
			ToolCalls: []elelem.ToolCall{{ID: "example"}},
		}},
	})
	require.NoError(t, err)
	ctx := context.WithValue(t.Context(), workflowActivityLeaseContextKey{},
		session.ActivityLease{Attempt: 1})
	prepared := &preparedTurn{publisher: essessey.NewPublisher(ctx,
		rejectedWorkflowStreamSink{})}
	adapter, err := prepared.workflowToolAdapter(ctx, workflowToolInput{
		Round: workflowRoundInput{Execution: workflowExecutionInput{
			Context: encoded,
		}},
	})
	require.NoError(t, err)
	err = adapter.OnToolCallStart(ctx, elelem.ToolCallEvent{
		CallID: "example", Name: toolNameReadFile,
	})
	require.ErrorIs(t, err, commerr.ErrFetchFailed)
}

func TestToolStreamOffsetsRejectInvalidSlots(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		stream  *workflowToolStream
		index   int
		attempt int64
	}{
		{name: "missing", attempt: 1},
		{name: "negative base", stream: &workflowToolStream{Base: -1, Count: 1}, attempt: 1},
		{name: "zero count", stream: &workflowToolStream{}, attempt: 1},
		{name: "negative index", stream: &workflowToolStream{Count: 1}, index: -1, attempt: 1},
		{name: "past batch", stream: &workflowToolStream{Count: 1}, index: 1, attempt: 1},
		{name: "zero attempt", stream: &workflowToolStream{Count: 1}},
		{name: "count overflow", stream: &workflowToolStream{Count: math.MaxInt}, attempt: 1},
		{name: "attempt overflow", stream: &workflowToolStream{Count: 1}, attempt: math.MaxInt64},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := tc.stream.offset(tc.index, tc.attempt)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}
