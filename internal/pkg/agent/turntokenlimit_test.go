package agent

import (
	"encoding/json"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const turnTokenLimitTestLimit = 1000

func TestCheckTurnTokenLimit(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		limits  *workflowExecutionLimits
		usage   elelem.Usage
		wantErr bool
	}{
		{name: "no limits saved", usage: tokenUsage(turnTokenLimitTestLimit * 2)},
		{
			name:   "zero limit",
			limits: &workflowExecutionLimits{},
			usage:  tokenUsage(turnTokenLimitTestLimit * 2),
		},
		{
			name:   "nothing used",
			limits: tokenLimits(),
		},
		{
			name:   "one token under",
			limits: tokenLimits(),
			usage:  tokenUsage(turnTokenLimitTestLimit - 1),
		},
		{
			name:    "exactly at the limit",
			limits:  tokenLimits(),
			usage:   tokenUsage(turnTokenLimitTestLimit),
			wantErr: true,
		},
		{
			name:    "over the limit",
			limits:  tokenLimits(),
			usage:   tokenUsage(turnTokenLimitTestLimit + 1),
			wantErr: true,
		},
		{
			name:   "failed attempts count",
			limits: tokenLimits(),
			usage: elelem.Usage{
				TokenCounts: elelem.TokenCounts{
					Total: turnTokenLimitTestLimit / 2,
				},
				Retry: elelem.RetryInfo{
					WastedTotalTokens: turnTokenLimitTestLimit / 2,
				},
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := checkTurnTokenLimit(workflowConversation{
				Limits: tc.limits, Usage: tc.usage,
			})
			if !tc.wantErr {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, ErrTurnTokenLimit)
			require.ErrorIs(t, err, commerr.ErrOutOfRange)
		})
	}
}

// A root round whose turn already billed its limit never reaches the
// provider and records no model call. One under the limit runs as usual.
func TestRootWorkflowModelStopsAtTheTurnTokenLimit(t *testing.T) {
	t.Parallel()

	t.Run("under the limit", func(t *testing.T) {
		t.Parallel()
		driver := elelemtest.NewScriptedDriver(elelemtest.Text("done"))
		fixture, _ := newRootModelWorkflowFixture(t, driver)
		input := workflowRoundInput{Execution: fixture.input}
		input.Execution.Context = withTurnTokens(t,
			input.Execution.Context, turnTokenLimitTestLimit-1)

		_, err := fixture.engine.modelRound(t.Context(), input)
		require.NoError(t, err)
		assert.Equal(t, 1, driver.Calls())
	})

	t.Run("at the limit", func(t *testing.T) {
		t.Parallel()
		driver := elelemtest.NewScriptedDriver(elelemtest.Text("done"))
		fixture, prepared := newRootModelWorkflowFixture(t, driver)
		input := workflowRoundInput{Execution: fixture.input}
		input.Execution.Context = withTurnTokens(t,
			input.Execution.Context, turnTokenLimitTestLimit)

		_, err := fixture.base.runtime.workflowRootModelRound(t.Context(),
			prepared, input)
		require.ErrorIs(t, err, ErrTurnTokenLimit)
		assert.Zero(t, driver.Calls())
		runs, err := fixture.base.store.ListModelRuns(t.Context(),
			fixture.input.SessionID, session.ListModelRunsOptions{})
		require.NoError(t, err)
		assert.Empty(t, runs.Items)
	})
}

func tokenLimits() *workflowExecutionLimits {
	return &workflowExecutionLimits{MaxTurnTokens: turnTokenLimitTestLimit}
}

func tokenUsage(total int64) elelem.Usage {
	return elelem.Usage{TokenCounts: elelem.TokenCounts{Total: total}}
}

// withTurnTokens returns a saved root context whose rounds billed used tokens
// against the test limit.
func withTurnTokens(
	t *testing.T,
	saved json.RawMessage,
	used int64,
) json.RawMessage {
	t.Helper()

	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(saved, &conversation))
	require.NotNil(t, conversation.Limits)
	conversation.Limits.MaxTurnTokens = turnTokenLimitTestLimit
	conversation.Usage = tokenUsage(used)

	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)

	return encoded
}
