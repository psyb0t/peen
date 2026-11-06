package agent

import (
	"context"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/metrics"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type summarizingStorage struct {
	session.Storage
	queries []metrics.SummaryQuery
}

func (s *summarizingStorage) QueryMetrics(
	_ context.Context,
	query metrics.SummaryQuery,
) (metrics.SummaryResult, error) {
	s.queries = append(s.queries, query)

	return metrics.SummaryResult{Summary: query.Summary}, nil
}

func TestReadMetricsAsksTheWorkerStore(t *testing.T) {
	t.Parallel()

	storage := &summarizingStorage{}
	deps := &launchAgentDeps{runtime: &Runtime{store: storage}}
	query := metrics.SummaryQuery{Summary: metrics.SummaryTools, Limit: 5}

	result, err := deps.readMetrics(t.Context(), query)
	require.NoError(t, err)

	assert.Equal(t, metrics.SummaryTools, result.Summary)
	assert.Equal(t, []metrics.SummaryQuery{query}, storage.queries)
}

func TestReadMetricsFailsInARuntimeWithoutStoredMetrics(t *testing.T) {
	t.Parallel()

	deps := &launchAgentDeps{runtime: &Runtime{}}

	_, err := deps.readMetrics(
		t.Context(),
		metrics.SummaryQuery{Summary: metrics.SummaryTools},
	)
	require.ErrorIs(t, err, commerr.ErrNotImplemented)
}
