package db

import (
	"context"
	"database/sql"
	"path/filepath"

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

// OpenMetricsStorage opens metrics.db and applies its embedded migrations.
func OpenMetricsStorage(
	ctx context.Context,
	cfg Config,
) (*MetricsStorage, error) {
	path, busyTimeout, err := prepareSQLiteFile(cfg, MetricsFileName)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "prepare metrics sqlite")
	}

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

	runtimecontext.Logger(ctx).Info("metrics sqlite ready")

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
