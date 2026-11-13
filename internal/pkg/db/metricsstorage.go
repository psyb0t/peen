package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"

	commonsqlite "github.com/psyb0t/common-go/db/sqlite"
	sqlitegorm "github.com/psyb0t/common-go/db/sqlite/gorm"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/migrations"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	// MetricsFileName is the observation database under the state directory.
	MetricsFileName = "metrics.db"

	metricsMigrationsPath = "metrics"
	metricsStaleSuffix    = ".stale"
	migrationUpSuffix     = ".up.sql"
	sqliteWALSuffix       = "-wal"
	sqliteSHMSuffix       = "-shm"
	sqliteSyncFull        = "FULL"
	// sqliteSyncNormal keeps metrics.db consistent across a crash and may lose
	// the last few writes, which suits disposable telemetry.
	sqliteSyncNormal = "NORMAL"
)

// MetricsStorage owns metrics.db, kept apart from peen.db so telemetry writes
// never contend with the transcript and the file can be deleted on its own.
type MetricsStorage struct {
	Query *repositories.Query
	sqlDB *sql.DB
}

// OpenMetricsStorage opens metrics.db and applies its embedded migrations. The
// file is disposable, so one whose schema differs from what this build expects
// is moved aside to metrics.db.stale and a new one takes its place.
func OpenMetricsStorage(
	ctx context.Context,
	cfg Config,
) (*MetricsStorage, error) {
	path, busyTimeout, err := prepareSQLiteFile(cfg, MetricsFileName)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "prepare metrics sqlite")
	}

	storage, err := openMetricsFile(ctx, path, busyTimeout)
	if err != nil {
		return nil, err
	}

	current, err := hasCurrentMetricsSchema(ctx, storage.sqlDB)
	if err != nil {
		return nil, closeAfterOpenFailure(ctx, storage.sqlDB, err)
	}

	if current {
		runtimecontext.Logger(ctx).Info("metrics sqlite ready")

		return storage, nil
	}

	runtimecontext.Logger(ctx).Warn(
		"metrics.db has a different schema, starting a new file",
		"reason", "metrics_schema_mismatch",
		"stale_file", path+metricsStaleSuffix,
	)

	if err := storage.Close(); err != nil {
		return nil, ctxerrors.Wrap(err, "close the mismatched metrics sqlite")
	}

	if err := retireMetricsFile(path); err != nil {
		return nil, err
	}

	if _, _, err := prepareSQLiteFile(cfg, MetricsFileName); err != nil {
		return nil, ctxerrors.Wrap(err, "prepare the new metrics sqlite")
	}

	return openMetricsFile(ctx, path, busyTimeout)
}

// hasCurrentMetricsSchema compares the open file with the schema the embedded
// migrations build.
func hasCurrentMetricsSchema(
	ctx context.Context,
	sqlDB *sql.DB,
) (bool, error) {
	statements, err := metricsMigrationStatements()
	if err != nil {
		return false, err
	}

	expected, err := memorySchemaSnapshot(ctx, statements)
	if err != nil {
		return false, err
	}

	actual, err := schemaSnapshot(ctx, sqlDB)
	if err != nil {
		return false, err
	}

	return actual == expected, nil
}

func metricsMigrationStatements() ([]string, error) {
	entries, err := migrations.MetricsFS.ReadDir(metricsMigrationsPath)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list metrics migrations")
	}

	statements := make([]string, 0, len(entries))

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), migrationUpSuffix) {
			continue
		}

		content, err := migrations.MetricsFS.ReadFile(
			metricsMigrationsPath + "/" + entry.Name(),
		)
		if err != nil {
			return nil, ctxerrors.Wrapf(
				err, "read metrics migration %q", entry.Name(),
			)
		}

		statements = append(statements, string(content))
	}

	return statements, nil
}

// retireMetricsFile moves a closed file aside, replacing an earlier stale one.
// A leftover write-ahead log would replay into the new file, so it goes too.
func retireMetricsFile(path string) error {
	if err := os.Rename(path, path+metricsStaleSuffix); err != nil {
		return ctxerrors.Wrap(err, "move the mismatched metrics sqlite aside")
	}

	for _, suffix := range []string{sqliteWALSuffix, sqliteSHMSuffix} {
		err := os.Remove(path + suffix)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return ctxerrors.Wrapf(
				err, "remove the stale metrics sqlite %s file", suffix,
			)
		}
	}

	return nil
}

func openMetricsFile(
	ctx context.Context,
	path string,
	busyTimeout int64,
) (*MetricsStorage, error) {
	database, err := gorm.Open(
		sqlitegorm.Open(sqliteDSNWithSync(path, busyTimeout, sqliteSyncNormal)),
		&gorm.Config{Logger: logger.Discard, TranslateError: true},
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open metrics sqlite")
	}

	sqlDB, err := database.DB()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get metrics sqlite sql db")
	}

	if err := configureSQLite(ctx, sqlDB); err != nil {
		return nil, closeAfterOpenFailure(ctx, sqlDB, err)
	}

	if err := installDatabaseMetrics(
		database, sqlDB, nil, MetricsFileName,
	); err != nil {
		return nil, closeAfterOpenFailure(ctx, sqlDB, err)
	}

	if err := commonsqlite.MigrateUp(
		sqlDB,
		metricsMigrationsPath,
		&migrations.MetricsFS,
	); err != nil {
		return nil, closeAfterOpenFailure(
			ctx, sqlDB, ctxerrors.Wrap(err, "migrate metrics sqlite"),
		)
	}

	if err := verifySQLiteIntegrity(ctx, sqlDB); err != nil {
		return nil, closeAfterOpenFailure(ctx, sqlDB, err)
	}

	return &MetricsStorage{
		Query: repositories.Use(database),
		sqlDB: sqlDB,
	}, nil
}

// DatabaseFiles names every SQLite file under the state directory by its role.
func DatabaseFiles(stateDirectory string) map[string]string {
	return map[string]string{
		"sessions":  filepath.Join(stateDirectory, sqliteFileName),
		"workflows": filepath.Join(stateDirectory, workflowSQLiteFileName),
		"metrics":   filepath.Join(stateDirectory, MetricsFileName),
	}
}

// Close releases the connection. Repeated calls are safe.
func (s *MetricsStorage) Close() error {
	if s == nil || s.sqlDB == nil {
		return nil
	}

	return closeSQLDB(s.sqlDB)
}
