package agent

import (
	"encoding/json"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cutOffToolArguments = `{"path":"notes/no`

func TestValidToolCallsReplacesArgumentsThatDoNotParse(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		arguments string
		want      string
	}{
		{
			name: "valid arguments are kept", arguments: `{"path":"notes.txt"}`,
			want: `{"path":"notes.txt"}`,
		},
		{
			name: "arguments cut off mid-call", arguments: cutOffToolArguments,
			want: emptyToolArguments,
		},
		{name: "no arguments at all", arguments: "", want: emptyToolArguments},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := []elelem.ToolCall{{
				ID: "call-1", Name: toolNameReadFile,
				Arguments: json.RawMessage(tc.arguments),
			}}

			valid := validToolCalls(calls)
			require.Len(t, valid, 1)
			assert.JSONEq(t, tc.want, string(valid[0].Arguments))
			assert.Equal(t, tc.arguments, string(calls[0].Arguments),
				"the caller's calls are not changed")
		})
	}
}

// A reply cut off at the output limit inside a tool call left arguments that
// do not parse, and saving the round failed on them.
func TestWorkflowModelRoundSavesAToolCallCutOffMidArguments(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Turn{
		Deltas: []elelem.Delta{{
			ToolCall: &elelem.ToolCallDelta{
				Index: 0, ID: "call-1", Name: toolNameReadFile,
				Arguments: cutOffToolArguments,
			},
			FinishReason: elelem.FinishReasonLength,
		}},
	})
	fixture := newExecutionLoopFixture(t, driver, true)
	request, saved, err := workflowModelRequest(fixture.base.runtime.models,
		workflowRoundInput{Execution: fixture.input})
	require.NoError(t, err)

	result, err := workflowRunModelRequest(t.Context(), request, saved)
	require.NoError(t, err)
	require.Len(t, result.Calls, 1)
	assert.JSONEq(t, emptyToolArguments, string(result.Calls[0].Arguments))

	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(result.Context, &conversation))
	last := conversation.Messages[len(conversation.Messages)-1]
	require.Len(t, last.ToolCalls, 1)
	assert.JSONEq(t, emptyToolArguments, string(last.ToolCalls[0].Arguments))
}
