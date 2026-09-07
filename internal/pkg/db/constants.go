package db

import (
	"io/fs"
	"time"
)

const (
	defaultSQLiteBusyTimeout = 5 * time.Second
	sqliteDirectoryMode      = fs.FileMode(0o700)
	sqliteFileMode           = fs.FileMode(0o600)
	sqliteMigrationsPath     = "sqlite"
)
