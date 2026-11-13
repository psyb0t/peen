package db

import (
	"io/fs"
	"time"
)

const (
	// WorkflowContinuationLimit bounds reads even for a cyclic history.
	WorkflowContinuationLimit    = 1024
	sqliteDriverName             = "sqlite"
	defaultSQLiteBusyTimeout     = 5 * time.Second
	sqliteSlowOperationThreshold = 200 * time.Millisecond
	sqliteReadConnections        = 4
	sqliteReadCallback           = "peen:sqliteReads"
	sqliteReadRestoreCallback    = "peen:sqliteRestoreWriter"
	sqliteDirectoryMode          = fs.FileMode(0o700)
	sqliteFileMode               = fs.FileMode(0o600)
	sqliteMigrationsPath         = "sqlite"
	// workflowTaskWait bounds how long an empty workflow task claim waits
	// for a write before it claims again.
	workflowTaskWait = time.Second
)
