package agent

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/go-workflows/workflow"
	"github.com/psyb0t/peen/internal/pkg/publicerror"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	turnLimitTestRounds       = 2
	turnLimitTestTokenLimit   = 10
	turnLimitTestRoundTokens  = 12
	turnLimitTestListArgument = `{}`
	// turnLimitTestLocationMarker opens the source location ctxerrors adds.
	turnLimitTestLocationMarker = "["
)

func turnLimitToolRound(id string, tokens int64) elelemtest.Turn {
	return elelemtest.ToolCall(id, toolNameListFiles,
		turnLimitTestListArgument).WithUsage(elelem.Usage{
		TokenCounts: elelem.TokenCounts{Total: tokens},
	})
}

// failedTurnError returns the error text of the session's only turn.failed
// event, which is what a client reads.
func failedTurnError(t *testing.T, base runtimeFixture) string {
	t.Helper()

	page, err := base.store.ListEvents(t.Context(), base.runtime.sessionID,
		session.ListEventsOptions{
			EventTypes: []string{string(EventTypeTurnFailed)},
			Limit:      session.MaximumPageLimit,
		})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)

	var payload turnFailedPayload

	require.NoError(t,
		json.Unmarshal([]byte(page.Items[0].PayloadJSON), &payload))

	return payload.Error
}

// A root that keeps calling tools stops at its round limit with the limit's
// own text, and the failure still matches the round limit sentinel.
func TestNativeRootTurnStopsAtItsRoundLimit(t *testing.T) {
	t.Parallel()

	driver := elelemtest.NewScriptedDriver(
		turnLimitToolRound("first", 1),
		turnLimitToolRound("second", 1),
		elelemtest.Text("must not run"),
	)
	base := newNativeRunnerFixtureWithOptions(t, driver,
		func(options *RuntimeOptions) {
			options.MaxToolRounds = turnLimitTestRounds
		})

	_, err := base.runtime.Run(t.Context(), TurnRequest{
		RequestID: uuid.New(), Message: "keep calling tools",
	})
	require.ErrorIs(t, err, session.ErrTurnRoundLimit)
	require.ErrorIs(t, err, commerr.ErrOutOfRange)
	require.ErrorIs(t, err, elelem.ErrMaxRoundsExceeded)
	assert.Equal(t, turnLimitTestRounds, driver.Calls())

	want := fmt.Sprintf(rootRoundLimitFormat, turnLimitTestRounds)
	assert.Equal(t, want, failedTurnError(t, base))
	assert.Equal(t, want, publicerror.Message(err))
	assert.NotContains(t, failedTurnError(t, base),
		turnLimitTestLocationMarker)
}

// A root whose rounds billed the token limit stops before the next round
// with the limit's own text.
func TestNativeRootTurnStopsAtItsTokenLimit(t *testing.T) {
	t.Parallel()

	driver := elelemtest.NewScriptedDriver(
		turnLimitToolRound("first", turnLimitTestRoundTokens),
		elelemtest.Text("must not run"),
	)
	base := newNativeRunnerFixtureWithOptions(t, driver,
		func(options *RuntimeOptions) {
			options.MaxTurnTokens = turnLimitTestTokenLimit
		})

	_, err := base.runtime.Run(t.Context(), TurnRequest{
		RequestID: uuid.New(), Message: "use the tokens",
	})
	require.ErrorIs(t, err, session.ErrTurnTokenLimit)
	require.ErrorIs(t, err, ErrTurnTokenLimit)
	require.ErrorIs(t, err, commerr.ErrOutOfRange)
	assert.Equal(t, 1, driver.Calls())

	want := fmt.Sprintf(rootTokenLimitFormat, turnLimitTestRoundTokens,
		turnLimitTestTokenLimit)
	assert.Equal(t, want, failedTurnError(t, base))
	assert.NotContains(t, failedTurnError(t, base),
		turnLimitTestLocationMarker)
}

// The type names the restore step matches are the names go-workflows records.
func TestWorkflowErrorTypeNamesMatchTheRecordedTypes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		err  error
		want string
	}{
		{"round limit", &roundLimitError{}, workflowErrorTypeRoundLimit},
		{"token limit", &tokenLimitError{}, workflowErrorTypeTokenLimit},
		{
			"provider refusal", &providerRefusalError{},
			workflowErrorTypeProviderRefusal,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var recorded *workflow.Error

			require.ErrorAs(t, workflow.NewError(tc.err), &recorded)
			assert.Equal(t, tc.want, recorded.Type)
		})
	}
}

// A failure that crossed the workflow history keeps only text and type names.
// Restoring it brings back the sentinel and the public text.
func TestRestoreWorkflowSentinelsAfterTheHistoryRoundTrip(t *testing.T) {
	t.Parallel()

	limit := newRoundLimitError(turnLimitTestRounds, false)
	wrapped := ctxerrors.Wrap(limit,
		"phased workflow has no continuation round")

	encoded, err := json.Marshal(workflow.NewError(wrapped))
	require.NoError(t, err)

	var recorded workflow.Error

	require.NoError(t, json.Unmarshal(encoded, &recorded))
	require.NotErrorIs(t, &recorded, session.ErrTurnRoundLimit)

	restored := restoreWorkflowSentinels(ctxerrors.Wrap(&recorded,
		"wait for native root result"))
	require.ErrorIs(t, restored, session.ErrTurnRoundLimit)
	require.ErrorIs(t, restored, commerr.ErrOutOfRange)
	assert.Equal(t, limit.Error(), publicerror.Message(restored))

	plain := ctxerrors.New("unrelated failure")
	assert.Equal(t, plain, restoreWorkflowSentinels(plain))
	assert.NoError(t, restoreWorkflowSentinels(nil))
}

// A child's failure detail is the limit's text, with no source location, and
// names the child setting and the limit the child was launched with.
func TestChildRoundLimitFailureDetailHasNoSourceLocation(t *testing.T) {
	t.Parallel()

	const childRounds = 5

	input := workflowExecutionInput{
		MaxRounds: turnLimitTestRounds,
		Context: json.RawMessage(fmt.Sprintf(
			`{"child":{"maxRounds":%d}}`, childRounds)),
	}

	cause := ctxerrors.Wrap(workflowRoundLimitError(input),
		"phased workflow has no continuation round")
	require.Contains(t, cause.Error(), turnLimitTestLocationMarker)

	want := fmt.Sprintf(childRoundLimitFormat, childRounds)
	assert.Equal(t, want, publicerror.Message(restoreWorkflowSentinels(cause)))

	reported := workflowFailureCause(cause)
	assert.Equal(t, want, reported.Error())
	require.ErrorIs(t, reported, session.ErrTurnRoundLimit)
	assert.NotContains(t, reported.Error(), turnLimitTestLocationMarker)
}

func TestCheckWorkflowTurnTokensReadsTheSavedUsage(t *testing.T) {
	t.Parallel()

	conversation := workflowConversation{
		Limits: tokenLimits(),
		Usage:  tokenUsage(turnTokenLimitTestLimit),
	}
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)

	err = checkWorkflowTurnTokens(workflowExecutionInput{Context: encoded})
	require.ErrorIs(t, err, ErrTurnTokenLimit)

	var limit *tokenLimitError

	require.ErrorAs(t, err, &limit)
	assert.Equal(t, fmt.Sprintf(rootTokenLimitFormat, turnTokenLimitTestLimit,
		turnTokenLimitTestLimit), limit.Error())

	conversation.Usage = tokenUsage(turnTokenLimitTestLimit - 1)
	encoded, err = json.Marshal(conversation)
	require.NoError(t, err)
	require.NoError(t,
		checkWorkflowTurnTokens(workflowExecutionInput{Context: encoded}))
}
