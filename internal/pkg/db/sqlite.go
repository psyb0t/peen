package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	commonsqlite "github.com/psyb0t/common-go/db/sqlite"
	sqlitegorm "github.com/psyb0t/common-go/db/sqlite/gorm"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/migrations"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	sqliteFileName   = "peen.db"
	sqliteMemoryPath = ":memory:"
)

// Open opens Peen's SQLite store, migrates it, and verifies its integrity.
func Open(ctx context.Context, cfg Config) (*Handle, error) {
	databasePath, busyTimeout, err := prepareSQLite(cfg)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "prepare sqlite")
	}

	database, err := gorm.Open(
		sqlitegorm.Open(sqliteDSN(databasePath, busyTimeout)),
		&gorm.Config{
			Logger:         logger.Discard,
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

	if err := verifyMigrationLineage(ctx, sqlDB); err != nil {
		return nil, closeAfterOpenFailure(ctx, sqlDB, err)
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

	runtimecontext.Logger(ctx).Info("sqlite ready", "driver", sqliteDriverName)

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
		runtimecontext.Logger(ctx).Warn(
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
	return prepareSQLiteFile(cfg, sqliteFileName)
}

func prepareSQLiteFile(cfg Config, fileName string) (string, int64, error) {
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

	if cfg.InMemory {
		return sqliteMemoryPath, busyTimeout.Milliseconds(), nil
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

	databasePath := filepath.Join(directoryPath, fileName)
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

// sqliteTableOrder lists the tables in the order their create migrations run,
// read from the migration file names
// `<7-digit sequence>_create_<table>_table.up.sql`. Migration N creates entry
// N-1, so a database at version N holds exactly the first N tables.
func sqliteTableOrder() ([]string, error) {
	entries, err := migrations.SQLiteFS.ReadDir(sqliteMigrationsPath)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list sqlite migrations")
	}

	order := []string{}

	for _, entry := range entries {
		stem, isUp := strings.CutSuffix(entry.Name(), ".up.sql")
		if !isUp {
			continue
		}

		sequence, rest, _ := strings.Cut(stem, "_create_")

		table, isTable := strings.CutSuffix(rest, "_table")
		if !isTable || sequence != fmt.Sprintf("%07d", len(order)+1) {
			return nil, ctxerrors.Wrapf(
				commerr.ErrInvalidState,
				"sqlite migration %q is not the next create migration",
				entry.Name(),
			)
		}

		order = append(order, table)
	}

	return order, nil
}

// verifyMigrationLineage refuses a database that a different Peen version
// created. Those versions overlap the current numbering, so the recorded
// version alone cannot tell them apart. The tables present must be exactly the
// ones the recorded version should have created, and their columns, defaults,
// constraints, and indexes must match the migrations up to that version.
func verifyMigrationLineage(ctx context.Context, sqlDB *sql.DB) error {
	version, dirty, recorded, err := recordedMigrationVersion(ctx, sqlDB)
	if err != nil {
		return err
	}

	if !recorded {
		return nil
	}

	tables, err := migratedTableNames(ctx, sqlDB)
	if err != nil {
		return err
	}

	order, err := sqliteTableOrder()
	if err != nil {
		return err
	}

	if !tablesMatchVersion(tables, order, version, dirty) {
		return errIncompatibleDatabase(version)
	}

	// A migration that stopped midway leaves a schema between two versions.
	// That stays a migration problem, reported by the runner.
	if dirty {
		return nil
	}

	return verifyRecordedSchema(ctx, sqlDB, version)
}

// tablesMatchVersion reports whether tables are exactly the ones the first
// version migrations create. A migration that failed midway leaves the version
// marked dirty with the tables of the previous step.
func tablesMatchVersion(
	tables, order []string,
	version int,
	dirty bool,
) bool {
	if version < 0 || version > len(order) {
		return false
	}

	if tablesMatchPrefix(tables, order[:version]) {
		return true
	}

	return dirty && version >= 1 && tablesMatchPrefix(tables, order[:version-1])
}

func tablesMatchPrefix(tables, prefix []string) bool {
	if len(tables) != len(prefix) {
		return false
	}

	expected := make(map[string]struct{}, len(prefix))
	for _, name := range prefix {
		expected[name] = struct{}{}
	}

	for _, name := range tables {
		if _, ok := expected[name]; !ok {
			return false
		}
	}

	return true
}

// recordedMigrationVersion reads the version the migration runner stored. It
// reports recorded as false for a database the runner has not touched.
func recordedMigrationVersion(
	ctx context.Context,
	sqlDB *sql.DB,
) (int, bool, bool, error) {
	var tableCount int
	if err := sqlDB.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sqlite_master
		 WHERE type = 'table' AND name = 'schema_migrations'`,
	).Scan(&tableCount); err != nil {
		return 0, false, false, ctxerrors.Wrap(
			err,
			"look up sqlite migration table",
		)
	}

	if tableCount == 0 {
		return 0, false, false, nil
	}

	var (
		version int
		dirty   bool
	)

	err := sqlDB.QueryRowContext(
		ctx,
		"SELECT version, dirty FROM schema_migrations LIMIT 1",
	).Scan(&version, &dirty)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, false, nil
	}

	if err != nil {
		return 0, false, false, ctxerrors.Wrap(
			err,
			"read sqlite migration version",
		)
	}

	return version, dirty, true, nil
}

// migratedTableNames lists the tables the migrations created, excluding the
// objects SQLite and the migration runner own.
func migratedTableNames(
	ctx context.Context,
	sqlDB *sql.DB,
) ([]string, error) {
	rows, err := sqlDB.QueryContext(
		ctx,
		`SELECT name FROM sqlite_master
		 WHERE type = 'table'
		   AND name NOT LIKE 'sqlite_%'
		   AND name <> 'schema_migrations'`,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list sqlite tables")
	}

	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			runtimecontext.Logger(ctx).Warn(
				"close sqlite table rows",
				"err",
				closeErr,
			)
		}
	}()

	names := []string{}

	for rows.Next() {
		name := ""
		if err := rows.Scan(&name); err != nil {
			return nil, ctxerrors.Wrap(err, "scan sqlite table name")
		}

		names = append(names, name)
	}

	if err := rows.Err(); err != nil {
		return nil, ctxerrors.Wrap(err, "iterate sqlite tables")
	}

	return names, nil
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
			runtimecontext.Logger(ctx).Warn(
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
