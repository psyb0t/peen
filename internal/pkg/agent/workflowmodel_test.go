package agent

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/psyb0t/ctxerrors"
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

func TestWorkflowModelRoundReleasesPlanAfterDroppingHistory(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name             string
		maxContextTokens int
		wantPlan         bool
		wantSent         int
	}{
		{
			name: "history that fits keeps the plan", maxContextTokens: 1_000_000,
			wantPlan: true, wantSent: 4,
		},
		{
			name: "dropped history releases the plan", maxContextTokens: 2_000,
			wantPlan: false, wantSent: 2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			driver := elelemtest.NewScriptedDriver(elelemtest.Text("done"))
			fixture := newExecutionLoopFixture(t, driver, true)
			source, messages := workflowCompactionStateFixture(t)
			detail := elelem.Text(strings.Repeat("completed work detail ", 2_000))
			messages[1].Content, messages[2].Content = detail, detail
			var conversation workflowConversation
			require.NoError(t, json.Unmarshal(fixture.input.Context,
				&conversation))
			conversation.SystemPrompt = messages[0].Text()
			conversation.Messages = slices.Concat(messages[1:], []elelem.Message{{
				Role: elelem.RoleUser, Content: elelem.Text("current request"),
			}})
			conversation.Compaction = snapshotWorkflowCompaction(source)
			conversation.Tools = nil
			conversation.MaxContextTokens = tc.maxContextTokens
			conversation.OutputReserveTokens = 0
			encoded, err := json.Marshal(conversation)
			require.NoError(t, err)
			fixture.input.Context = encoded
			request, saved, err := workflowModelRequest(
				fixture.base.runtime.models,
				workflowRoundInput{Execution: fixture.input})
			require.NoError(t, err)
			result, err := workflowRunModelRequest(t.Context(), request, saved)
			require.NoError(t, err)
			requests := driver.Requests()
			require.Len(t, requests, 1)
			assert.Len(t, requests[0].Messages, tc.wantSent)
			var next workflowConversation
			require.NoError(t, json.Unmarshal(result.Context, &next))
			assert.Equal(t, tc.wantPlan, next.Compaction != nil)
			require.NoError(t, validateWorkflowCompactionConversation(next))
		})
	}
}

// A provider call that succeeded after a retry reports the failed attempt with
// its error. The saved round must still decode when its tool results merge.
func TestWorkflowModelRoundSavesUsageOfARetriedCall(t *testing.T) {
	t.Parallel()
	usage := elelem.Usage{Retry: elelem.RetryInfo{
		TotalAttempts: 2,
		FailedAttempts: []elelem.RetryAttempt{{
			Attempt: 1, Reason: elelem.RetryReasonServerError,
			Err:    ctxerrors.Wrap(commerr.ErrFetchFailed, "provider stream"),
			Status: http.StatusInternalServerError,
		}},
	}}
	driver := elelemtest.NewScriptedDriver(elelemtest.ToolCall(
		"call-1", toolNameReadFile, `{"path":"notes.txt"}`,
	).WithUsage(usage))
	fixture := newExecutionLoopFixture(t, driver, true)
	request, saved, err := workflowModelRequest(fixture.base.runtime.models,
		workflowRoundInput{Execution: fixture.input})
	require.NoError(t, err)
	result, err := workflowRunModelRequest(t.Context(), request, saved)
	require.NoError(t, err)
	require.Len(t, result.Calls, 1)

	merged, err := workflowMergeRound(result.Context,
		[]workflowToolInput{{Call: result.Calls[0]}},
		[]workflowToolResult{{Result: elelem.ToolResult{Content: "notes"}}})
	require.NoError(t, err)

	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(merged, &conversation))
	assert.Equal(t, 2, conversation.Usage.Retry.TotalAttempts)
	require.Len(t, conversation.Usage.Retry.FailedAttempts, 1)
	attempt := conversation.Usage.Retry.FailedAttempts[0]
	assert.Equal(t, elelem.RetryReasonServerError, attempt.Reason)
	assert.Equal(t, http.StatusInternalServerError, attempt.Status)
}

func TestWorkflowModelRoundRecordsFinishReason(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name string
		turn elelemtest.Turn
		want elelem.FinishReason
	}{
		{
			name: "a finished reply", turn: elelemtest.Text("done"),
			want: elelem.FinishReasonStop,
		},
		{
			name: "a reply cut off at the output limit",
			turn: elelemtest.Turn{Deltas: []elelem.Delta{{
				Reasoning: "still thinking", FinishReason: elelem.FinishReasonLength,
			}}},
			want: elelem.FinishReasonLength,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			driver := elelemtest.NewScriptedDriver(tc.turn)
			fixture := newExecutionLoopFixture(t, driver, true)
			request, saved, err := workflowModelRequest(
				fixture.base.runtime.models,
				workflowRoundInput{Execution: fixture.input})
			require.NoError(t, err)
			result, err := workflowRunModelRequest(t.Context(), request, saved)
			require.NoError(t, err)
			assert.Equal(t, tc.want, result.FinishReason)
		})
	}
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
