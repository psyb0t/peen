# SQLite GORM adapter

This package uses `modernc.org/sqlite` with GORM's SQLite dialect and works with `CGO_ENABLED=0`.

```go
import (
    sqlitegorm "github.com/psyb0t/common-go/db/sqlite/gorm"
    "gorm.io/gorm"
)

database, err := gorm.Open(
    sqlitegorm.Open("application.db?_pragma=foreign_keys(1)"),
    &gorm.Config{TranslateError: true},
)
```

Check `err` before using `database`. The caller closes the underlying `*sql.DB` returned by `database.DB()`. Pass that connection to the existing `github.com/psyb0t/common-go/db/sqlite` migration helpers.

With `TranslateError` enabled, primary-key and unique violations return `gorm.ErrDuplicatedKey`; foreign-key violations return `gorm.ErrForeignKeyViolated`. SQLite foreign-key enforcement must be enabled in the DSN as shown. Other errors retain their native identity and can be inspected with `errors.As` and `*modernc.org/sqlite.Error`. With translation disabled, all driver errors remain native.

Importing this adapter registers modernc's `sqlite` database/sql driver. It can coexist with go-workflows, which uses the same driver. Do not also import `github.com/glebarez/go-sqlite`, which registers the same name. Applications using only `db/sqlite` migrations keep choosing their own driver.
