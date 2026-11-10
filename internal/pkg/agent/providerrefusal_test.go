package agent

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/go-workflows/workflow"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// providerRefusalTestBody stands for the provider's own error body, which
	// the turn text must not carry.
	providerRefusalTestBody = "provider body with account detail"
	phasedWorkflowTestText  = "phased workflow"
)

func TestProviderRefusalIsPermanentAndOtherFailuresRetry(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		err    error
		reason string
	}{
		{
			name: "not authenticated", err: commerr.ErrNotAuthenticated,
			reason: providerRefusalAuth,
		},
		{
			name: "http status",
			err: &elelem.ProviderError{
				Cause:      ctxerrors.New(providerRefusalTestBody),
				StatusCode: http.StatusUnauthorized,
			},
			reason: "HTTP 401 Unauthorized",
		},
		{
			name: "server error after elelem retried",
			err: &elelem.ProviderError{
				Cause:      ctxerrors.New(providerRefusalTestBody),
				StatusCode: http.StatusServiceUnavailable,
			},
			reason: "HTTP 503 Service Unavailable",
		},
		{
			name: "quota exhausted", err: elelem.ErrQuotaExhausted,
			reason: providerRefusalQuota,
		},
		{
			name: "context exceeded", err: elelem.ErrContextExceeded,
			reason: providerRefusalContext,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			wrapped := ctxerrors.Wrap(tc.err, "workflow activity interrupted")
			failure := permanentModelFailure(wrapped)
			assert.False(t, workflow.CanRetry(failure))

			restored := restoreWorkflowSentinels(workflow.NewError(failure))

			var refusal *providerRefusalError

			require.ErrorAs(t, restored, &refusal)
			assert.Equal(t, fmt.Sprintf(providerRefusalFormat, tc.reason),
				refusal.Error())
			assert.NotContains(t, refusal.Error(), providerRefusalTestBody)
		})
	}
}

// A failure that is not a provider answer stays retryable, and a stopped
// activity is never mistaken for a refusal.
func TestOtherModelFailuresStayRetryable(t *testing.T) {
	t.Parallel()

	for _, err := range []error{commerr.ErrFetchFailed, context.Canceled} {
		wrapped := ctxerrors.Wrap(err, "workflow activity interrupted")
		assert.False(t, isProviderRefusal(wrapped))
		assert.Equal(t, wrapped, permanentModelFailure(wrapped))
	}

	assert.True(t, workflow.CanRetry(
		permanentModelFailure(ctxerrors.Wrap(commerr.ErrFetchFailed, "store"))))
}

func TestTokenLimitIsPermanentInTheModelActivity(t *testing.T) {
	t.Parallel()

	limit := ctxerrors.Wrap(newTokenLimitError(2, 1, false), "check")
	failure := permanentModelFailure(limit)
	assert.False(t, workflow.CanRetry(failure))

	restored := restoreWorkflowSentinels(workflow.NewError(failure))
	require.ErrorIs(t, restored, session.ErrTurnTokenLimit)
}

// A provider that answers 401 produces one model run and one call that was
// never retried, and the turn reports the refusal instead of the workflow
// that carried it.
func TestNativeRootTurnProviderRefusalRunsOnce(t *testing.T) {
	t.Parallel()

	driver := elelemtest.NewScriptedDriver(
		elelemtest.Turn{Err: commerr.ErrNotAuthenticated},
		elelemtest.Turn{Err: commerr.ErrNotAuthenticated},
		elelemtest.Turn{Err: commerr.ErrNotAuthenticated},
	)
	base := newNativeRunnerFixture(t, driver)

	_, err := base.runtime.Run(t.Context(), TurnRequest{
		RequestID: uuid.New(), Message: "call the refusing provider",
	})
	require.Error(t, err)
	assert.Equal(t, 1, driver.Calls())

	runs, err := base.store.ListModelRuns(t.Context(), base.runtime.sessionID,
		session.ListModelRunsOptions{})
	require.NoError(t, err)
	require.Len(t, runs.Items, 1)

	calls, err := base.store.ListModelCalls(t.Context(),
		base.runtime.sessionID, runs.Items[0].ID,
		session.ListModelCallsOptions{})
	require.NoError(t, err)
	require.Len(t, calls.Items, 1)
	assert.Zero(t, calls.Items[0].RetryAttemptCount)

	failure := failedTurnError(t, base)
	assert.Equal(t,
		fmt.Sprintf(providerRefusalFormat, providerRefusalAuth), failure)
	assert.NotContains(t, failure, phasedWorkflowTestText)
}
