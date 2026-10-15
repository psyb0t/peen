package agent

import (
	"encoding/json"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowMergeRoundKeepsToolResultsContiguous(t *testing.T) {
	t.Parallel()
	calls := []workflowToolInput{
		{Call: elelem.ToolCall{ID: "first", Name: toolNameReadFile}},
		{Call: elelem.ToolCall{ID: "second", Name: toolNameReadFile}},
	}
	encoded, err := json.Marshal(workflowConversation{
		Messages: []elelem.Message{{
			Role:      elelem.RoleAssistant,
			ToolCalls: []elelem.ToolCall{calls[0].Call, calls[1].Call},
		}},
	})
	require.NoError(t, err)
	merged, err := workflowMergeRound(encoded, calls, []workflowToolResult{
		{
			Result: elelem.ToolResult{Content: "first result"},
			Injections: []elelem.MessageInjection{{
				Type: elelem.RoleUser, Content: "hook context",
			}},
		},
		{Result: elelem.ToolResult{Content: "second result"}},
	})
	require.NoError(t, err)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(merged, &conversation))
	require.Len(t, conversation.Messages, 4)
	assert.Equal(t, elelem.RoleTool, conversation.Messages[1].Role)
	assert.Equal(t, "first", conversation.Messages[1].ToolCallID)
	assert.Equal(t, elelem.RoleTool, conversation.Messages[2].Role)
	assert.Equal(t, "second", conversation.Messages[2].ToolCallID)
	assert.Equal(t, elelem.RoleUser, conversation.Messages[3].Role)
	assert.Equal(t, "hook context", conversation.Messages[3].Text())
}

func TestWorkflowModelRequestPreservesActiveInjections(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("done"))
	fixture := newExecutionLoopFixture(t, driver, true)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(fixture.input.Context, &conversation))
	conversation.Messages = append(conversation.Messages, elelem.Message{
		Role: elelem.RoleUser, Content: elelem.Text("active hook instructions"),
		Origin: elelem.MessageOriginInjection,
		Injection: &elelem.MessageInjection{
			Type: elelem.RoleUser, Content: "active hook instructions",
		},
	})
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	fixture.input.Context = encoded
	request, saved, err := workflowModelRequest(fixture.base.runtime.models,
		workflowRoundInput{Execution: fixture.input})
	require.NoError(t, err)
	_, err = workflowRunModelRequest(t.Context(), request, saved)
	require.NoError(t, err)
	requests := driver.Requests()
	require.Len(t, requests, 1)
	require.Len(t, requests[0].Messages, 3)
	assert.Equal(t, "active hook instructions", requests[0].Messages[2].Text())
}

func TestWorkflowMergeRoundRejectsMismatchedResults(t *testing.T) {
	t.Parallel()
	_, err := workflowMergeRound(json.RawMessage(`{}`),
		[]workflowToolInput{{}}, nil)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	_, err = workflowMergeRound(json.RawMessage(`{}`), nil,
		[]workflowToolResult{{}})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}
