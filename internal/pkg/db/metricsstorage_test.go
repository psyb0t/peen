package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	staleMetricsTable  = "stale_marker"
	staleMetricsCreate = "CREATE TABLE stale_marker (id INTEGER PRIMARY KEY)"
)

// openMetrics opens metrics.db in directory and closes it with the test.
func openMetrics(t *testing.T, directory string) *MetricsStorage {
	t.Helper()

	storage, err := OpenMetricsStorage(
		context.Background(),
		Config{Directory: directory},
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.Close()) })

	return storage
}

// writeMismatchedMetrics leaves a file with a table the migrations do not know,
// the shape another build's file has.
func writeMismatchedMetrics(t *testing.T, directory string) {
	t.Helper()

	storage, err := OpenMetricsStorage(
		context.Background(),
		Config{Directory: directory},
	)
	require.NoError(t, err)

	_, err = storage.sqlDB.ExecContext(t.Context(), staleMetricsCreate)
	require.NoError(t, err)
	require.NoError(t, storage.Close())
}

func metricsFileObjects(t *testing.T, path string) []string {
	t.Helper()

	sqlDB, err := sql.Open(sqliteDriverName, path)
	require.NoError(t, err)

	defer func() { require.NoError(t, sqlDB.Close()) }()

	return schemaObjectNames(t, sqlDB)
}

// The migrations are the schema of metrics.db, so a change to them has to be
// deliberate.
func TestMetricsSchemaMatchesPinnedSchema(t *testing.T) {
	t.Parallel()

	storage := openMetrics(t, t.TempDir())

	golden, err := os.ReadFile("testdata/metrics_schema.golden")
	require.NoError(t, err)

	assert.Equal(t, string(golden), dumpSchema(t, storage.sqlDB))
}

func TestOpenMetricsStorageKeepsAFileWithTheCurrentSchema(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()

	first, err := OpenMetricsStorage(
		context.Background(),
		Config{Directory: directory},
	)
	require.NoError(t, err)
	require.NoError(t, first.Close())

	openMetrics(t, directory)

	assert.NoFileExists(
		t,
		filepath.Join(directory, MetricsFileName+metricsStaleSuffix),
	)
}

func TestOpenMetricsStorageReplacesAFileWithADifferentSchema(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, MetricsFileName)

	writeMismatchedMetrics(t, directory)

	storage := openMetrics(t, directory)

	assert.Contains(
		t,
		metricsFileObjects(t, path+metricsStaleSuffix),
		staleMetricsTable,
	)
	assert.NotContains(
		t,
		schemaObjectNames(t, storage.sqlDB),
		staleMetricsTable,
	)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, sqliteFileMode, info.Mode().Perm())
}

func TestOpenMetricsStorageReplacesAnEarlierStaleFile(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, MetricsFileName)

	require.NoError(t, os.WriteFile(
		path+metricsStaleSuffix, []byte("old"), sqliteFileMode,
	))

	writeMismatchedMetrics(t, directory)
	openMetrics(t, directory)

	assert.Contains(
		t,
		metricsFileObjects(t, path+metricsStaleSuffix),
		staleMetricsTable,
	)
}
