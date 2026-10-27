package db

import (
	"io/fs"
	"time"
)

const (
	sqliteDriverName         = "sqlite"
	defaultSQLiteBusyTimeout = 5 * time.Second
	sqliteDirectoryMode      = fs.FileMode(0o700)
	sqliteFileMode           = fs.FileMode(0o600)
	sqliteMigrationsPath     = "sqlite"
	// workflowTaskWait bounds how long an empty workflow task claim waits
	// for a write before it claims again.
	workflowTaskWait = time.Second
)
