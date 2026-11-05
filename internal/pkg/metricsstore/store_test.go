package metricsstore

import (
	"context"
	"testing"
	"time"

	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testRetention = time.Hour
	testToolName  = "readFile"
)

func openStorage(t *testing.T, directory string) *db.MetricsStorage {
	t.Helper()

	storage, err := db.OpenMetricsStorage(
		t.Context(),
		db.Config{Directory: directory},
	)
	require.NoError(t, err)

	return storage
}

func readRows(
	t *testing.T,
	directory string,
) []*models.MetricObservation {
	t.Helper()

	storage := openStorage(t, directory)
	t.Cleanup(func() { require.NoError(t, storage.Close()) })

	repository := storage.Query.MetricObservation

	rows, err := repository.WithContext(t.Context()).
		Order(repository.ID).
		Find()
	require.NoError(t, err)

	return rows
}

func TestCloseWritesWhatIsQueued(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	observedAt := time.Now()

	store, err := Start(context.Background(), Options{
		Storage:   openStorage(t, directory),
		Retention: testRetention,
	})
	require.NoError(t, err)

	store.Add(metrics.Observation{
		Kind:               metrics.KindModel,
		At:                 observedAt,
		SessionID:          "session",
		WorkerGenerationID: "generation",
		TurnID:             "turn",
		AgentRunID:         "run",
		Name:               "provider/model",
		Stage:              "turn",
		Function:           "runProvider",
		Outcome:            metrics.OutcomeSuccess,
		DurationMS:         1500,
		FirstTokenMS:       200,
		InputTokens:        10,
		OutputTokens:       4,
		CacheReadTokens:    3,
		CacheWriteTokens:   2,
		Retries:            1,
	})
	require.NoError(t, store.Close())
	require.NoError(t, store.Close())

	rows := readRows(t, directory)
	require.Len(t, rows, 1)

	row := rows[0]
	assert.Equal(t, observedAt.UnixMilli(), row.ObservedAtMs)
	assert.Equal(t, string(metrics.KindModel), row.Kind)
	assert.Equal(t, "session", row.SessionID)
	assert.Equal(t, "generation", row.WorkerGenerationID)
	assert.Equal(t, "turn", row.TurnID)
	assert.Equal(t, "run", row.AgentRunID)
	assert.Equal(t, "provider/model", row.Name)
	assert.Equal(t, "runProvider", row.FunctionName)
	assert.Equal(t, int64(1500), row.DurationMs)
	assert.Equal(t, int64(200), row.FirstTokenMs)
	assert.Equal(t, int64(10), row.InputTokens)
	assert.Equal(t, int64(4), row.OutputTokens)
	assert.Equal(t, int64(3), row.CacheReadTokens)
	assert.Equal(t, int64(2), row.CacheWriteTokens)
	assert.Equal(t, int64(1), row.Retries)
}

func TestBatchesBiggerThanOneWriteAllLand(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	total := batchSize*2 + 1

	store, err := Start(context.Background(), Options{
		Storage:   openStorage(t, directory),
		Retention: testRetention,
	})
	require.NoError(t, err)

	for range total {
		store.Add(metrics.Observation{
			Kind: metrics.KindTool,
			At:   time.Now(),
			Name: testToolName,
		})
	}

	require.NoError(t, store.Close())
	assert.Len(t, readRows(t, directory), total)
}

func TestStartupPrunesObservationsPastRetention(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	now := time.Now()

	first, err := Start(context.Background(), Options{
		Storage:   openStorage(t, directory),
		Retention: 24 * time.Hour,
	})
	require.NoError(t, err)

	first.Add(metrics.Observation{
		Kind: metrics.KindTool,
		At:   now.Add(-2 * testRetention),
		Name: "old",
	})
	first.Add(metrics.Observation{
		Kind: metrics.KindTool,
		At:   now,
		Name: testToolName,
	})
	require.NoError(t, first.Close())
	require.Len(t, readRows(t, directory), 2)

	second, err := Start(context.Background(), Options{
		Storage:   openStorage(t, directory),
		Retention: testRetention,
	})
	require.NoError(t, err)
	require.NoError(t, second.Close())

	rows := readRows(t, directory)
	require.Len(t, rows, 1)
	assert.Equal(t, testToolName, rows[0].Name)
}

func TestFullQueueDropsAndCounts(t *testing.T) {
	t.Parallel()

	collector := metrics.New()
	store := &Store{
		metrics: collector,
		queue:   make(chan metrics.Observation, 1),
	}

	store.Add(metrics.Observation{Kind: metrics.KindTool})
	store.Add(metrics.Observation{Kind: metrics.KindTool})

	families, err := collector.Registry().Gather()
	require.NoError(t, err)

	var dropped float64

	for _, family := range families {
		if family.GetName() != "peen_metrics_observations_dropped_total" {
			continue
		}

		dropped = family.GetMetric()[0].GetCounter().GetValue()
	}

	assert.Equal(t, float64(1), dropped)
}

func TestFailedWriteDropsTheBatchWithoutPanicking(t *testing.T) {
	t.Parallel()

	storage := openStorage(t, t.TempDir())
	require.NoError(t, storage.Close())

	store := &Store{storage: storage, metrics: metrics.New()}

	batch := store.write(t.Context(), []*models.MetricObservation{
		{Kind: string(metrics.KindTool)},
	})

	assert.Empty(t, batch)
}

func TestStartNeedsStorage(t *testing.T) {
	t.Parallel()

	_, err := Start(context.Background(), Options{})

	require.Error(t, err)
}
