package migrations

import "embed"

//go:embed sqlite/*.sql
var SQLiteFS embed.FS

// MetricsFS holds the schema of metrics.db, which has its own migration
// history because it is a separate, disposable file.
//
//go:embed metrics/*.sql
var MetricsFS embed.FS
