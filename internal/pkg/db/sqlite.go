package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/glebarez/sqlite"
	commondb "github.com/psyb0t/common-go/db"
	commonsqlite "github.com/psyb0t/common-go/db/sqlite"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db/migrations"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"gorm.io/gorm"
)

const sqliteFileName = "peen.db"

// Open opens Peen's SQLite store, migrates it, and verifies its integrity.
func Open(ctx context.Context, cfg Config) (*Handle, error) {
	databasePath, busyTimeout, err := prepareSQLite(cfg)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "prepare sqlite")
	}

	database, err := gorm.Open(
		sqlite.Open(sqliteDSN(databasePath, busyTimeout)),
		&gorm.Config{
			Logger:         commondb.NewGormSlogLogger(),
			TranslateError: true,
		},
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open sqlite")
	}

	sqlDB, err := database.DB()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get sqlite sql db")
	}

	if err := configureSQLite(ctx, sqlDB); err != nil {
		return nil, closeAfterOpenFailure(ctx, sqlDB, err)
	}

	if err := installDatabaseMetrics(database, sqlDB, cfg.Metrics); err != nil {
		return nil, closeAfterOpenFailure(
			ctx,
			sqlDB,
			ctxerrors.Wrap(err, "install database metrics"),
		)
	}

	if err := commonsqlite.MigrateUp(
		sqlDB,
		sqliteMigrationsPath,
		&migrations.SQLiteFS,
	); err != nil {
		return nil, closeAfterOpenFailure(
			ctx,
			sqlDB,
			ctxerrors.Wrap(err, "migrate sqlite"),
		)
	}

	if err := verifySQLiteIntegrity(ctx, sqlDB); err != nil {
		return nil, closeAfterOpenFailure(ctx, sqlDB, err)
	}

	repositories.SetDefault(database)
	ctxscope.GetLogger(ctx).Info("sqlite ready", "driver", "sqlite")

	return &Handle{
		GormDB: database,
		sqlDB:  sqlDB,
	}, nil
}

func closeSQLDB(sqlDB *sql.DB) error {
	if err := sqlDB.Close(); err != nil {
		return ctxerrors.Wrap(err, "close sqlite")
	}

	return nil
}

func closeAfterOpenFailure(
	ctx context.Context,
	sqlDB *sql.DB,
	operationErr error,
) error {
	if closeErr := closeSQLDB(sqlDB); closeErr != nil {
		ctxscope.GetLogger(ctx).Warn(
			"close sqlite after open failure",
			"err",
			closeErr,
		)
	}

	return operationErr
}

func configureSQLite(ctx context.Context, sqlDB *sql.DB) error {
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	if err := sqlDB.PingContext(ctx); err != nil {
		return ctxerrors.Wrap(err, "ping sqlite")
	}

	return nil
}

func prepareSQLite(cfg Config) (string, int64, error) {
	busyTimeout := cfg.BusyTimeout
	if busyTimeout == 0 {
		busyTimeout = defaultSQLiteBusyTimeout
	}

	if busyTimeout < 0 {
		return "", 0, ctxerrors.Wrapf(
			commerr.ErrInvalidArgument,
			"sqlite busy timeout must not be negative, got %s",
			busyTimeout,
		)
	}

	if cfg.Directory == "" ||
		!filepath.IsAbs(cfg.Directory) ||
		hasUnsafeSQLitePathCharacters(cfg.Directory) {
		return "", 0, ctxerrors.Wrapf(
			commerr.ErrInvalidArgument,
			"sqlite directory must be an absolute non-empty path, got %q",
			cfg.Directory,
		)
	}

	directoryPath := filepath.Clean(cfg.Directory)
	if err := ensurePrivateDirectory(directoryPath); err != nil {
		return "", 0, ctxerrors.Wrap(err, "prepare sqlite directory")
	}

	databasePath := filepath.Join(directoryPath, sqliteFileName)
	if err := ensurePrivateDatabaseFile(databasePath); err != nil {
		return "", 0, ctxerrors.Wrap(err, "prepare sqlite database file")
	}

	return databasePath, busyTimeout.Milliseconds(), nil
}

func ensurePrivateDirectory(directoryPath string) error {
	if err := os.MkdirAll(directoryPath, sqliteDirectoryMode); err != nil {
		return ctxerrors.Wrapf(err, "create sqlite directory %q", directoryPath)
	}

	directoryInfo, err := os.Lstat(directoryPath)
	if err != nil {
		return ctxerrors.Wrapf(
			err,
			"inspect sqlite directory %q",
			directoryPath,
		)
	}

	if !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 {
		return ctxerrors.Wrapf(
			commerr.ErrInvalidArgument,
			"sqlite directory must be a real directory, got %q",
			directoryPath,
		)
	}

	if err := os.Chmod(directoryPath, sqliteDirectoryMode); err != nil {
		return ctxerrors.Wrapf(
			err,
			"restrict sqlite directory mode %q",
			directoryPath,
		)
	}

	return nil
}

func ensurePrivateDatabaseFile(databasePath string) error {
	fileInfo, err := os.Lstat(databasePath)
	if os.IsNotExist(err) {
		file, createErr := os.OpenFile(
			databasePath,
			os.O_CREATE|os.O_EXCL|os.O_WRONLY,
			sqliteFileMode,
		)
		if createErr != nil {
			return ctxerrors.Wrapf(
				createErr,
				"create sqlite database %q",
				databasePath,
			)
		}

		if closeErr := file.Close(); closeErr != nil {
			return ctxerrors.Wrapf(
				closeErr,
				"close new sqlite database %q",
				databasePath,
			)
		}

		return nil
	}

	if err != nil {
		return ctxerrors.Wrapf(err, "inspect sqlite database %q", databasePath)
	}

	if !isSQLiteFileMode(fileInfo.Mode()) {
		return ctxerrors.Wrapf(
			commerr.ErrInvalidArgument,
			"sqlite database must be a regular non-symlink file, got %q",
			databasePath,
		)
	}

	if err := os.Chmod(databasePath, sqliteFileMode); err != nil {
		return ctxerrors.Wrapf(
			err,
			"restrict sqlite database mode %q",
			databasePath,
		)
	}

	return nil
}

func sqliteDSN(databasePath string, busyTimeoutMilliseconds int64) string {
	return fmt.Sprintf(
		//nolint:lll // DSN query parameters are one indivisible connection string.
		"%s?_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)",
		databasePath,
		busyTimeoutMilliseconds,
	)
}

func verifySQLiteIntegrity(ctx context.Context, sqlDB *sql.DB) error {
	if err := assertIntegrityCheck(ctx, sqlDB); err != nil {
		return err
	}

	return assertForeignKeys(ctx, sqlDB)
}

func assertIntegrityCheck(ctx context.Context, sqlDB *sql.DB) error {
	var integrityResult string
	if err := sqlDB.QueryRowContext(
		ctx,
		"PRAGMA integrity_check",
	).Scan(&integrityResult); err != nil {
		return ctxerrors.Wrap(err, "run sqlite integrity check")
	}

	if integrityResult != "ok" {
		return ctxerrors.Wrapf(
			commerr.ErrInvalidState,
			"sqlite integrity check failed: %s",
			integrityResult,
		)
	}

	return nil
}

func assertForeignKeys(ctx context.Context, sqlDB *sql.DB) error {
	rows, err := sqlDB.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return ctxerrors.Wrap(err, "run sqlite foreign key check")
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			ctxscope.GetLogger(ctx).Warn(
				"close sqlite foreign key rows",
				"err",
				closeErr,
			)
		}
	}()

	if rows.Next() {
		return ctxerrors.Wrap(
			commerr.ErrInvalidState,
			"sqlite foreign key check failed",
		)
	}

	if err := rows.Err(); err != nil {
		return ctxerrors.Wrap(err, "iterate sqlite foreign key check")
	}

	return nil
}

func isSQLiteFileMode(mode fs.FileMode) bool {
	return mode.IsRegular() && mode&os.ModeSymlink == 0
}

func hasUnsafeSQLitePathCharacters(path string) bool {
	return strings.ContainsAny(path, "?#")
}
