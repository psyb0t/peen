// Package db opens Peen's SQLite database, applies its embedded migrations,
// and wires the generated gorm/gen repositories. Business code uses the typed
// repository layer rather than a raw database handle.
package db

import (
	"database/sql"
	"time"

	"github.com/psyb0t/peen/internal/pkg/metrics"
	"gorm.io/gorm"
)

// Config identifies the private directory and SQLite connection behavior.
type Config struct {
	Directory   string
	BusyTimeout time.Duration
	Metrics     *metrics.Metrics
}

// Handle owns an open SQLite connection and its generated GORM query surface.
type Handle struct {
	GormDB *gorm.DB
	sqlDB  *sql.DB
}

// Close releases the database resources held by the handle.
func (h *Handle) Close() error {
	if h == nil || h.sqlDB == nil {
		return nil
	}

	return closeSQLDB(h.sqlDB)
}

// SQLDB exposes the infrastructure connection for migration and integrity
// verification. Request-path code must use generated repositories instead.
func (h *Handle) SQLDB() *sql.DB {
	if h == nil {
		return nil
	}

	return h.sqlDB
}
