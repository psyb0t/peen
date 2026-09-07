package migrations

import "embed"

//go:embed sqlite/*.sql
var SQLiteFS embed.FS
