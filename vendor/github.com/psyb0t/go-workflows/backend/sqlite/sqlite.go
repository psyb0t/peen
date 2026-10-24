package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"

	"github.com/psyb0t/go-workflows/backend"
	backenddb "github.com/psyb0t/go-workflows/backend/sqlite/db"
)

type sqliteBackend struct {
	*backenddb.Store
	db             *sql.DB
	workerName     string
	options        *options
	ownsConnection bool
	memConn        *sql.Conn
}

var _ backend.Backend = (*sqliteBackend)(nil)

func NewInMemoryBackend(opts ...option) *sqliteBackend {
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_txlock=immediate", uuid.NewString())
	b := newSqliteBackend(dsn, opts...)
	b.db.SetConnMaxIdleTime(0)
	b.db.SetMaxIdleConns(1)
	b.db.SetMaxOpenConns(2)
	var err error
	b.memConn, err = b.db.Conn(context.Background())
	if err != nil {
		panic(ctxerrors.Wrap(errors.Join(err, b.db.Close()), "retain in-memory SQLite"))
	}
	return b
}

func NewSqliteBackend(path string, opts ...option) *sqliteBackend {
	return newSqliteBackend(fmt.Sprintf("file:%s?_txlock=immediate", path), opts...)
}

func newSqliteBackend(dsn string, opts ...option) *sqliteBackend {
	pool, err := sql.Open("sqlite", dsn+"&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		panic(ctxerrors.Wrap(err, "open SQLite backend"))
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	return wrapPool(pool, true, opts...)
}

// NewSqliteBackendWithDB leaves connection configuration and ownership with the caller.
func NewSqliteBackendWithDB(pool *sql.DB, opts ...option) *sqliteBackend {
	return wrapPool(pool, false, opts...)
}

func wrapPool(pool *sql.DB, owned bool, opts ...option) *sqliteBackend {
	settings := &options{Options: backend.ApplyOptions(), ApplyMigrations: owned}
	for _, option := range opts {
		option(settings)
	}
	name := getWorkerName(settings)
	store, err := backenddb.New(pool, name, settings.Options)
	if err != nil {
		if owned {
			err = errors.Join(err, pool.Close())
		}
		panic(ctxerrors.Wrap(err, "initialize SQLite backend"))
	}
	b := &sqliteBackend{Store: store, db: pool, workerName: name, options: settings, ownsConnection: owned}
	if settings.ApplyMigrations {
		if err := b.Migrate(); err != nil {
			if owned {
				err = errors.Join(err, pool.Close())
			}
			panic(ctxerrors.Wrap(err, "initialize SQLite migrations"))
		}
	}
	return b
}

func (b *sqliteBackend) Close() error {
	var closeError error
	if b.memConn != nil {
		closeError = b.memConn.Close()
	}
	if b.ownsConnection {
		closeError = errors.Join(closeError, b.db.Close())
	}
	if closeError != nil {
		return ctxerrors.Wrap(closeError, "close SQLite backend")
	}
	return nil
}

func (b *sqliteBackend) Migrate() error { return backenddb.MigrateUp(b.db) }

func getWorkerName(options *options) string {
	if options.WorkerName != "" {
		return options.WorkerName
	}
	return "worker-" + uuid.NewString()
}
