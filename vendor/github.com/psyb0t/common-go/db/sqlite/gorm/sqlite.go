// Package sqlitegorm provides a pure-Go SQLite dialector for GORM.
// Importing it registers modernc.org/sqlite with database/sql.
package sqlitegorm

import (
	"errors"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	modernsqlite "modernc.org/sqlite"
	sqlitecodes "modernc.org/sqlite/lib"
)

const driverName = "sqlite"

// Dialector uses modernc with GORM's SQLite query and schema support.
type Dialector struct {
	sqlite.Dialector
}

// Open configures a dialector. gorm.Open opens the connection.
// The caller closes its sql.DB. Enable TranslateError to map constraints.
func Open(dsn string) *Dialector {
	return &Dialector{Dialector: sqlite.Dialector{
		DriverName: driverName,
		DSN:        dsn,
	}}
}

// Translate maps native constraint codes to GORM sentinels and retains other
// errors. GORM's default translator cannot read modernc's private error fields.
func (d Dialector) Translate(err error) error {
	var native *modernsqlite.Error
	if !errors.As(err, &native) {
		return err
	}

	switch native.Code() {
	case sqlitecodes.SQLITE_CONSTRAINT_PRIMARYKEY,
		sqlitecodes.SQLITE_CONSTRAINT_UNIQUE:
		return gorm.ErrDuplicatedKey
	case sqlitecodes.SQLITE_CONSTRAINT_FOREIGNKEY:
		return gorm.ErrForeignKeyViolated
	default:
		return err
	}
}
