package protocol

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sessionRecordingReader struct {
	sessions []string
}

func (r *sessionRecordingReader) Summarize(
	_ context.Context,
	scope metrics.SummaryScope,
	query metrics.SummaryQuery,
) (metrics.SummaryResult, error) {
	r.sessions = append(r.sessions, scope.SessionID)

	return metrics.SummaryResult{Summary: query.Summary}, nil
}

// A worker cannot name a session: a forged field is ignored, and the read uses
// the session this connection authenticated.
func TestMetricsQueryReadsOnlyTheConnectionSession(t *testing.T) {
	t.Parallel()

	controller, sessionID := newControllerFixture(t)
	collector := metrics.New()
	reader := &sessionRecordingReader{}

	collector.SetSummaries(reader)

	controller.metrics = collector.NewWorkerIngest(
		sessionID, controller.generationID,
	)

	answer, err := controller.HandleCall(
		context.Background(),
		string(MethodQueryMetrics),
		json.RawMessage(`{"summary":"tools","sessionId":"another-session"}`),
	)
	require.NoError(t, err)

	result, ok := answer.(metrics.SummaryResult)
	require.True(t, ok)
	assert.Equal(t, metrics.SummaryTools, result.Summary)
	assert.Equal(t, []string{sessionID.String()}, reader.sessions)
}

func TestMetricsQueryFailsWhenMetricsAreUnavailable(t *testing.T) {
	t.Parallel()

	controller, _ := newControllerFixture(t)

	_, err := controller.HandleCall(
		context.Background(),
		string(MethodQueryMetrics),
		mustEncode(t, metrics.SummaryQuery{Summary: metrics.SummaryTools}),
	)
	require.ErrorIs(t, err, commerr.ErrNotImplemented)
}
