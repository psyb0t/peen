package metricsstore

import (
	"context"
	"testing"
	"time"

	"github.com/psyb0t/peen/internal/pkg/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testSessionID      = "session-a"
	testOtherSessionID = "session-b"
	testTurnID         = "turn-a"
	testAgentRunID     = "run-a"
	testModelName      = "provider/model"
	testSlowToolName   = "runCommand"
	testWindowMinutes  = 60
	testSummaryLimit   = 10
)

func startStore(t *testing.T) *Store {
	t.Helper()

	store, err := Start(context.Background(), Options{
		Storage:   openStorage(t, t.TempDir()),
		Retention: testRetention,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	return store
}

func summarize(
	t *testing.T,
	store *Store,
	query metrics.SummaryQuery,
) metrics.SummaryResult {
	t.Helper()

	return summarizeScope(
		t,
		store,
		metrics.SummaryScope{SessionID: testSessionID},
		query,
	)
}

func summarizeScope(
	t *testing.T,
	store *Store,
	scope metrics.SummaryScope,
	query metrics.SummaryQuery,
) metrics.SummaryResult {
	t.Helper()

	if query.WindowMinutes == 0 {
		query.WindowMinutes = testWindowMinutes
	}

	if query.Limit == 0 {
		query.Limit = testSummaryLimit
	}

	result, err := store.Summarize(t.Context(), scope, query)
	require.NoError(t, err)

	return result
}

func TestSummarizeFoldsOutcomesPerTool(t *testing.T) {
	t.Parallel()

	store := startStore(t)
	now := time.Now()

	for _, observation := range []metrics.Observation{
		{Name: testToolName, Outcome: metrics.OutcomeSuccess, DurationMS: 100},
		{Name: testToolName, Outcome: metrics.OutcomeError, DurationMS: 300},
		{Name: testToolName, Outcome: metrics.OutcomeCancelled, DurationMS: 50},
		{Name: testSlowToolName, Outcome: metrics.OutcomeSuccess, DurationMS: 900},
	} {
		observation.Kind = metrics.KindTool
		observation.At = now
		observation.SessionID = testSessionID
		store.Add(observation)
	}

	result := summarize(t, store, metrics.SummaryQuery{
		Summary: metrics.SummaryTools,
	})

	require.Len(t, result.Rows, 2)
	assert.Equal(t, testSlowToolName, result.Rows[0].Name)
	assert.False(t, result.Truncated)

	row := result.Rows[1]
	assert.Equal(t, testToolName, row.Name)
	assert.Equal(t, int64(3), row.Count)
	assert.Equal(t, int64(1), row.Failures)
	assert.Equal(t, int64(1), row.Cancelled)
	assert.Equal(t, int64(450), row.TotalDurationMS)
	assert.Equal(t, int64(300), row.MaxDurationMS)
}

func TestSummarizeReadsOnlyTheNamedSession(t *testing.T) {
	t.Parallel()

	store := startStore(t)

	store.Add(metrics.Observation{
		Kind:      metrics.KindTool,
		At:        time.Now(),
		SessionID: testOtherSessionID,
		Name:      testToolName,
	})

	result := summarize(t, store, metrics.SummaryQuery{
		Summary: metrics.SummaryTools,
	})

	assert.Empty(t, result.Rows)
}

func TestSummarizeAllSessionsGroupsBySession(t *testing.T) {
	t.Parallel()

	store := startStore(t)

	for _, sessionID := range []string{testSessionID, testOtherSessionID} {
		store.Add(metrics.Observation{
			Kind:      metrics.KindModel,
			At:        time.Now(),
			SessionID: sessionID,
			Name:      testModelName,
		})
	}

	result := summarizeScope(
		t,
		store,
		metrics.SummaryScope{AllSessions: true},
		metrics.SummaryQuery{Summary: metrics.SummarySessions},
	)

	names := make([]string, 0, len(result.Rows))
	for _, row := range result.Rows {
		names = append(names, row.Name)
	}

	assert.ElementsMatch(
		t,
		[]string{testSessionID, testOtherSessionID},
		names,
	)
}

func TestSummarizeLeavesOutRowsOlderThanTheWindow(t *testing.T) {
	t.Parallel()

	store := startStore(t)

	store.Add(metrics.Observation{
		Kind:      metrics.KindTool,
		At:        time.Now().Add(-2 * time.Minute),
		SessionID: testSessionID,
		Name:      testToolName,
	})

	result := summarize(t, store, metrics.SummaryQuery{
		Summary:       metrics.SummaryTools,
		WindowMinutes: 1,
	})

	assert.Empty(t, result.Rows)
}

func TestSummarizeGroupsModelTimeByEachSummary(t *testing.T) {
	t.Parallel()

	store := startStore(t)
	now := time.Now()

	for _, agentRunID := range []string{"", testAgentRunID} {
		store.Add(metrics.Observation{
			Kind:        metrics.KindModel,
			At:          now,
			SessionID:   testSessionID,
			TurnID:      testTurnID,
			AgentRunID:  agentRunID,
			Name:        testModelName,
			DurationMS:  100,
			InputTokens: 10,
			Retries:     1,
		})
	}

	testCases := []struct {
		name    string
		summary metrics.SummaryKind
		want    []string
	}{
		{
			name:    "models",
			summary: metrics.SummaryModels,
			want:    []string{testModelName},
		},
		{
			name:    "turns",
			summary: metrics.SummaryTurns,
			want:    []string{testTurnID},
		},
		{
			name:    "agents",
			summary: metrics.SummaryAgents,
			want:    []string{rootAgentName, testAgentRunID},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := summarize(t, store, metrics.SummaryQuery{
				Summary: tc.summary,
			})

			names := make([]string, 0, len(result.Rows))
			for _, row := range result.Rows {
				names = append(names, row.Name)
			}

			assert.ElementsMatch(t, tc.want, names)
			assert.Positive(t, result.Rows[0].InputTokens)
			assert.Positive(t, result.Rows[0].Retries)
		})
	}
}

func TestSummarizeTruncatesToTheLimit(t *testing.T) {
	t.Parallel()

	store := startStore(t)

	for _, name := range []string{"first", "second", "third"} {
		store.Add(metrics.Observation{
			Kind:      metrics.KindHook,
			At:        time.Now(),
			SessionID: testSessionID,
			Name:      name,
		})
	}

	result := summarize(t, store, metrics.SummaryQuery{
		Summary: metrics.SummaryHooks,
		Limit:   2,
	})

	assert.Len(t, result.Rows, 2)
	assert.True(t, result.Truncated)
}

func TestSummarizeRefusesAnUnknownSummary(t *testing.T) {
	t.Parallel()

	store := startStore(t)

	_, err := store.Summarize(
		t.Context(),
		metrics.SummaryScope{SessionID: testSessionID},
		metrics.SummaryQuery{
			Summary:       "everything",
			WindowMinutes: 1,
			Limit:         1,
		},
	)
	require.Error(t, err)
}

func TestSummarizeFailsInsteadOfHangingAfterClose(t *testing.T) {
	t.Parallel()

	store := startStore(t)
	require.NoError(t, store.Close())

	_, err := store.Summarize(
		t.Context(),
		metrics.SummaryScope{SessionID: testSessionID},
		metrics.SummaryQuery{
			Summary:       metrics.SummaryTools,
			WindowMinutes: 1,
			Limit:         1,
		},
	)
	require.Error(t, err)
}
