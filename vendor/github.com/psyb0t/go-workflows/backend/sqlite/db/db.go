// Package db owns sqlite connection setup and the backend's immutable migration history.
package db

import (
	"database/sql"

	commondb "github.com/psyb0t/common-go/db"
	commonsqlite "github.com/psyb0t/common-go/db/sqlite"
	sqlitegorm "github.com/psyb0t/common-go/db/sqlite/gorm"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"gorm.io/gorm"

	"github.com/psyb0t/go-workflows/backend/sqlite/db/migrations"
)

// Open wraps a caller-owned pool. It never changes ownership or creates schema.
func Open(pool *sql.DB) (*gorm.DB, error) {
	if pool == nil {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "database pool")
	}
	dialector := sqlitegorm.Open("")
	dialector.Conn = pool
	database, err := gorm.Open(dialector, &gorm.Config{
		Logger:                 commondb.NewGormSlogLogger(),
		SkipDefaultTransaction: true,
		DisableAutomaticPing:   true,
		TranslateError:         true,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open sqlite GORM")
	}
	return database, nil
}

// MigrateUp preserves the existing schema_migrations ledger and pool ownership.
func MigrateUp(pool *sql.DB) error {
	err := commonsqlite.MigrateUp(pool, ".", &migrations.FS)
	if err != nil {
		return ctxerrors.Wrap(err, "migrate sqlite backend")
	}
	return nil
}
