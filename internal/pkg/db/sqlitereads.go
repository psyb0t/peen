package db

import (
	"context"
	"database/sql"

	"github.com/psyb0t/ctxerrors"
	"gorm.io/gorm"
)

// openSQLiteReads shares a WAL file, but never the writer's connection queue.
// query_only applies to every connection, including replacements in the pool.
func openSQLiteReads(
	ctx context.Context,
	path string,
	busyTimeout int64,
) (*sql.DB, error) {
	reads, err := sql.Open(sqliteDriverName,
		sqliteDSN(path, busyTimeout)+"&_pragma=query_only(1)")
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open sqlite readers")
	}

	reads.SetMaxOpenConns(sqliteReadConnections)
	reads.SetMaxIdleConns(sqliteReadConnections)

	if err := reads.PingContext(ctx); err != nil {
		return nil, closeAfterOpenFailure(ctx, reads,
			ctxerrors.Wrap(err, "ping sqlite readers"))
	}

	return reads, nil
}

func (h *Handle) openReaders(
	ctx context.Context, path string, busyTimeout int64,
) error {
	reads, err := openSQLiteReads(ctx, path, busyTimeout)
	if err != nil {
		return err
	}

	if err := installSQLiteReads(h.GormDB, h.sqlDB, reads); err != nil {
		return closeAfterOpenFailure(ctx, reads, err)
	}

	h.readDB = reads

	return nil
}

// Queries inside a transaction or a pinned connection keep that connection.
// Restore the ordinary handle after scanning so reusing it for Begin or a
// write cannot accidentally select the query-only pool.
func installSQLiteReads(database *gorm.DB, writer, reads *sql.DB) error {
	selectPool := func(tx *gorm.DB) {
		if tx.Statement.ConnPool == writer {
			tx.Statement.ConnPool = reads
		}
	}
	restorePool := func(tx *gorm.DB) {
		if tx.Statement.ConnPool == reads {
			tx.Statement.ConnPool = writer
		}
	}

	registrations := []struct {
		before databaseMetricRegister
		after  databaseMetricRegister
	}{
		{
			database.Callback().Query().Before(gormQueryCallback).Register,
			database.Callback().Query().After("*").Register,
		},
		{
			database.Callback().Row().Before(gormRowCallback).Register,
			database.Callback().Row().After("*").Register,
		},
	}
	for _, registration := range registrations {
		if err := registration.before(
			sqliteReadCallback, selectPool,
		); err != nil {
			return ctxerrors.Wrap(err, "register sqlite reader selection")
		}

		if err := registration.after(sqliteReadRestoreCallback,
			restorePool); err != nil {
			return ctxerrors.Wrap(err, "register sqlite reader restoration")
		}
	}

	return nil
}
