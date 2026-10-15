package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowHookStateCopiesAndConsumesPerCall(t *testing.T) {
	t.Parallel()
	original := &toolHookRuntime{
		messages: map[string][]string{}, denials: map[string]string{},
	}
	original.add("first", []string{"first injection"})
	original.add("second", []string{"second injection"})
	original.deny(&elelem.ToolEvent{CallID: "blocked"}, "saved denial")
	saved := original.workflowState()
	original.add("first", []string{"later injection"})
	assert.Equal(t, []string{"first injection"}, saved.Messages["first"])
	encoded, err := json.Marshal(saved)
	require.NoError(t, err)
	var decoded workflowHookState
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	restored := &toolHookRuntime{}
	require.NoError(t, restored.restoreWorkflowState(&decoded))
	require.NoError(t, restored.restoreWorkflowState(&decoded))
	decoded.Messages["first"][0] = "changed after restoration"
	decoded.Denials["blocked"] = "changed after restoration"
	for _, callID := range []string{"first", "second"} {
		injection, err := restored.messageInjection(t.Context(),
			&elelem.ToolEvent{CallID: callID})
		require.NoError(t, err)
		require.NotNil(t, injection)
		assert.Equal(t, saved.Messages[callID][0], injection.Content)
	}

	assertHookDenialConsumedOnce(t, restored)
	assert.Nil(t, restored.workflowState())
	assert.NotNil(t, original.workflowState())
}

func assertHookDenialConsumedOnce(t *testing.T, restored *toolHookRuntime) {
	t.Helper()
	called := false
	handler := restored.handler(func(context.Context, elelem.ToolInput) (
		elelem.ToolResult, error,
	) {
		called = true

		return elelem.ToolResult{Content: "handler result"}, nil
	})
	result, err := handler(t.Context(), elelem.ToolInput{CallID: "blocked"})
	require.NoError(t, err)
	assert.False(t, called)
	assert.True(t, result.IsError)
	assert.Equal(t, "saved denial", result.Content)
	result, err = handler(t.Context(), elelem.ToolInput{CallID: "blocked"})
	require.NoError(t, err)
	assert.True(t, called)
	assert.False(t, result.IsError)
	assert.Equal(t, "handler result", result.Content)
}

func TestWorkflowHookStateRejectsMalformedCallsWithoutReplacingState(
	t *testing.T,
) {
	t.Parallel()
	testCases := []struct {
		name  string
		state workflowHookState
	}{
		{name: "empty message call", state: workflowHookState{
			Messages: map[string][]string{"": {"invalid"}},
		}},
		{name: "blank message call", state: workflowHookState{
			Messages: map[string][]string{" ": {"invalid"}},
		}},
		{name: "empty denial call", state: workflowHookState{
			Denials: map[string]string{"": "invalid"},
		}},
		{name: "blank denial call", state: workflowHookState{
			Denials: map[string]string{" ": "invalid"},
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runtime := &toolHookRuntime{messages: map[string][]string{}}
			runtime.add(hookInjectTestCallID, []string{hookInjectTestNote})
			err := runtime.restoreWorkflowState(&tc.state)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
			injection, err := runtime.messageInjection(t.Context(),
				&elelem.ToolEvent{CallID: hookInjectTestCallID})
			require.NoError(t, err)
			require.NotNil(t, injection)
			assert.Equal(t, hookInjectTestNote, injection.Content)
		})
	}
}

func TestWorkflowHookStateMissingCheckpointClearsPendingState(t *testing.T) {
	t.Parallel()
	runtime := &toolHookRuntime{
		messages: map[string][]string{"pending": {"stale injection"}},
		denials:  map[string]string{"pending": "stale denial"},
	}
	require.NoError(t, runtime.restoreWorkflowState(nil))
	assert.Nil(t, runtime.workflowState())
	runtime.add(hookInjectTestCallID, []string{hookInjectTestNote})
	assert.NotNil(t, runtime.workflowState())
	var missing *toolHookRuntime
	assert.Nil(t, missing.workflowState())
	require.NoError(t, missing.restoreWorkflowState(nil))
	require.ErrorIs(t, missing.restoreWorkflowState(&workflowHookState{}),
		commerr.ErrRequiredFieldNotSet)
}
