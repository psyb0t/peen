package agent

import (
	"strings"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	agentReportTestMaxTokens = 64
	agentReportTestLongWords = 2000
	agentReportTestWord      = "finding "
)

// Short answers and failures reach the parent agent unchanged.
func TestAgentReportTextKeepsShortOutcomes(t *testing.T) {
	t.Parallel()

	runtime := &Runtime{maxToolResultTokens: agentReportTestMaxTokens}

	testCases := []struct {
		name   string
		result AgentRunResult
		want   string
	}{
		{
			name: "short answer",
			result: AgentRunResult{
				State: AgentRunStateCompleted, Response: "done",
			},
			want: "done",
		},
		{
			name: "failure",
			result: AgentRunResult{
				State: AgentRunStateFailed, FailureDetail: "boom",
			},
			want: agentFailedLead + "boom",
		},
		{
			name:   "cancelled",
			result: AgentRunResult{State: AgentRunStateCancelled},
			want:   agentCancelledText,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := runtime.agentReportText(t.Context(), tc.result)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// A long final answer reaches the parent agent cut to the tool result
// limit, keeping its start, and says it was cut.
func TestAgentReportTextCutsALongAnswer(t *testing.T) {
	t.Parallel()

	runtime := &Runtime{maxToolResultTokens: agentReportTestMaxTokens}

	got, err := runtime.agentReportText(t.Context(), AgentRunResult{
		State:    AgentRunStateCompleted,
		Response: strings.Repeat(agentReportTestWord, agentReportTestLongWords),
	})
	require.NoError(t, err)

	require.True(t, strings.HasSuffix(got, agentReportCutNote))
	assert.True(t, strings.HasPrefix(got, agentReportTestWord))

	body := strings.TrimSuffix(got, agentReportCutNote)
	tokens, err := workflowContentTokens(elelem.DefaultTokenCounter(), body)
	require.NoError(t, err)
	assert.LessOrEqual(t, tokens, agentReportTestMaxTokens)
}
