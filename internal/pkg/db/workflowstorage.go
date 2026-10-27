package db

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/go-workflows/backend"
	workflowsqlite "github.com/psyb0t/go-workflows/backend/sqlite"
	"github.com/psyb0t/go-workflows/diag"
	"github.com/psyb0t/peen/internal/pkg/contentblob"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
)

const (
	workflowSQLiteFileName      = "workflows.db"
	workflowActivityLockTimeout = time.Minute
)

// WorkflowStorage owns the native workflow backend and its SQL connection.
// Its schema is separate from Peen's pinned session schema.
type WorkflowStorage struct {
	backend.Backend
	sqlDB     *sql.DB
	converter *contentblob.Converter
	// wake is shared by every backend this storage hands out, so a write
	// through any of them wakes a claim waiting in another.
	wake *workflowWake
}

// OpenWorkflowStorage prepares private SQLite storage and applies the native
// workflow backend's migrations. Startup errors are returned to the caller.
func OpenWorkflowStorage(
	ctx context.Context,
	cfg Config,
) (*WorkflowStorage, error) {
	var converter *contentblob.Converter
	if cfg.ContentBlobs != nil {
		converter = contentblob.NewConverter(cfg.ContentBlobs)
	}

	path, busyTimeout, err := prepareSQLiteFile(cfg, workflowSQLiteFileName)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "prepare workflow sqlite")
	}

	connection, err := sql.Open(sqliteDriverName, sqliteDSN(path, busyTimeout))
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open workflow sqlite")
	}

	if err := configureSQLite(ctx, connection); err != nil {
		return nil, closeAfterOpenFailure(ctx, connection, err)
	}

	// WithDB leaves ownership here and avoids the constructor that panics on
	// migration failure. Migrate exposes the same failure as an error.
	storage := workflowsqlite.NewSqliteBackendWithDB(
		connection,
		workflowsqlite.WithBackendOptions(workflowBackendOptions(ctx,
			converter)...),
	)
	if err := storage.Migrate(); err != nil {
		return nil, closeAfterOpenFailure(
			ctx, connection, ctxerrors.Wrap(err, "migrate workflow sqlite"),
		)
	}

	if err := verifySQLiteIntegrity(ctx, connection); err != nil {
		return nil, closeAfterOpenFailure(ctx, connection, err)
	}

	runtimecontext.Logger(ctx).Info("workflow sqlite ready")

	wake := newWorkflowWake()

	return &WorkflowStorage{
		Backend: &wakingBackend{Backend: storage, wake: wake},
		sqlDB:   connection, converter: converter, wake: wake,
	}, nil
}

// Close releases the caller-owned SQL connection. Repeated calls are safe.
func (s *WorkflowStorage) Close() error {
	if s == nil || s.sqlDB == nil {
		return nil
	}

	return closeSQLDB(s.sqlDB)
}

// WorkerBackend binds native task leases to one worker generation. The
// controller keeps the SQL connection; closing this borrowed backend does
// not close durable storage or apply migrations again.
//
//nolint:ireturn // The native backend implements this workflow interface.
func (s *WorkflowStorage) WorkerBackend(
	ctx context.Context,
	generationID uuid.UUID,
) (diag.Backend, error) {
	if s == nil || s.sqlDB == nil || generationID == uuid.Nil {
		return nil, ctxerrors.Wrap(
			commerr.ErrInvalidArgument,
			"workflow worker generation is required",
		)
	}

	if err := s.sqlDB.PingContext(ctx); err != nil {
		return nil, ctxerrors.Wrap(err, "check workflow worker storage")
	}

	options := append(workflowBackendOptions(ctx, s.converter),
		backend.WithWorkerName(generationID.String()))

	return &wakingBackend{
		Backend: workflowsqlite.NewSqliteBackendWithDB(
			s.sqlDB,
			workflowsqlite.WithBackendOptions(options...),
		),
		wake: s.wake,
	}, nil
}

// workflowBackendOptions compacts payloads when a converter is set, so a
// turn's workflow history stores each long string once.
func workflowBackendOptions(
	ctx context.Context,
	converter *contentblob.Converter,
) []backend.BackendOption {
	options := []backend.BackendOption{
		backend.WithLogger(runtimecontext.Logger(ctx)),
		backend.WithActivityLockTimeout(workflowActivityLockTimeout),
	}

	if converter != nil {
		options = append(options, backend.WithConverter(converter))
	}

	return options
}
