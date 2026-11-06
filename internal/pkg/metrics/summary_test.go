package metrics

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingReader struct {
	scope SummaryScope
	query SummaryQuery
}

func (r *recordingReader) Summarize(
	_ context.Context,
	scope SummaryScope,
	query SummaryQuery,
) (SummaryResult, error) {
	r.scope = scope
	r.query = query

	return SummaryResult{Summary: query.Summary}, nil
}

func TestSummarizeUsesTheConnectionSessionAndDefaults(t *testing.T) {
	t.Parallel()

	collector := New()
	reader := &recordingReader{}
	sessionID := uuid.New()

	collector.SetSummaries(reader)

	ingest := collector.NewWorkerIngest(sessionID, uuid.New())

	result, err := ingest.Summarize(
		t.Context(),
		SummaryQuery{Summary: SummaryTools},
	)
	require.NoError(t, err)

	assert.Equal(t, SummaryTools, result.Summary)
	assert.Equal(t, sessionID.String(), reader.scope.SessionID)
	assert.False(t, reader.scope.AllSessions)
	assert.Equal(t, defaultSummaryWindowMinutes, reader.query.WindowMinutes)
	assert.Equal(t, defaultSummaryRows, reader.query.Limit)
}

func TestReadAllSessionsWidensOnlyThatConnection(t *testing.T) {
	t.Parallel()

	collector := New()
	reader := &recordingReader{}

	collector.SetSummaries(reader)

	widened := collector.NewWorkerIngest(uuid.New(), uuid.New())
	widened.ReadAllSessions()

	_, err := widened.Summarize(
		t.Context(),
		SummaryQuery{Summary: SummarySessions},
	)
	require.NoError(t, err)
	assert.True(t, reader.scope.AllSessions)

	_, err = collector.NewWorkerIngest(uuid.New(), uuid.New()).Summarize(
		t.Context(),
		SummaryQuery{Summary: SummarySessions},
	)
	require.NoError(t, err)
	assert.False(t, reader.scope.AllSessions)
}

func TestSummarizeCapsTheLimit(t *testing.T) {
	t.Parallel()

	collector := New()
	reader := &recordingReader{}

	collector.SetSummaries(reader)

	_, err := collector.NewWorkerIngest(uuid.New(), uuid.New()).Summarize(
		t.Context(),
		SummaryQuery{Summary: SummaryHooks, Limit: MaxSummaryRows + 1},
	)
	require.NoError(t, err)

	assert.Equal(t, MaxSummaryRows, reader.query.Limit)
}

func TestSummarizeRefusesBadQueries(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		query SummaryQuery
	}{
		{name: "unknown summary", query: SummaryQuery{Summary: "everything"}},
		{
			name:  "negative window",
			query: SummaryQuery{Summary: SummaryTools, WindowMinutes: -1},
		},
		{
			name:  "negative limit",
			query: SummaryQuery{Summary: SummaryTools, Limit: -1},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			collector := New()

			collector.SetSummaries(&recordingReader{})

			_, err := collector.NewWorkerIngest(uuid.New(), uuid.New()).
				Summarize(t.Context(), tc.query)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}

func TestSummarizeFailsWithoutAReader(t *testing.T) {
	t.Parallel()

	collector := New()

	collector.SetSummaries(&recordingReader{})
	collector.SetSummaries(nil)

	_, err := collector.NewWorkerIngest(uuid.New(), uuid.New()).Summarize(
		t.Context(),
		SummaryQuery{Summary: SummaryTools},
	)
	require.ErrorIs(t, err, commerr.ErrNotImplemented)

	var missing *WorkerIngest

	_, err = missing.Summarize(t.Context(), SummaryQuery{Summary: SummaryTools})
	require.ErrorIs(t, err, commerr.ErrNotImplemented)
}

func TestWatchedGaugesAppearInTheRegistry(t *testing.T) {
	t.Parallel()

	collector := New()

	require.NoError(t, collector.WatchWebSocketClients(func() int { return 3 }))
	require.NoError(t, collector.WatchQueuedMessages(func() int64 { return 2 }))

	families, err := collector.registry.Gather()
	require.NoError(t, err)

	values := map[string]float64{}

	for _, family := range families {
		for _, metric := range family.GetMetric() {
			values[family.GetName()] = metric.GetGauge().GetValue()
		}
	}

	assert.InDelta(t, 3, values["peen_websocket_clients"], 0)
	assert.InDelta(t, 2, values["peen_queued_messages"], 0)
}
