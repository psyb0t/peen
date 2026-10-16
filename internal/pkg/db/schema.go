package db

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/migrations"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
)

const (
	// memoryDatabaseDSN opens a private in-memory database, used to build the
	// schema a recorded migration version should have.
	memoryDatabaseDSN = ":memory:"

	// checkKeyword starts a CHECK constraint inside a CREATE TABLE statement.
	checkKeyword = "CHECK"

	// indexOriginCreated is the origin pragma_index_list reports for an index
	// made by CREATE INDEX, as opposed to a constraint's implicit index.
	indexOriginCreated = "c"
)

// The patterns collapse formatting differences, so two schemas that differ only
// in whitespace compare equal.
var (
	whitespacePattern = regexp.MustCompile(`\s+`)
	openSpacePattern  = regexp.MustCompile(`\(\s+`)
	closeSpacePattern = regexp.MustCompile(`\s+\)`)
	commaSpacePattern = regexp.MustCompile(`\s*,\s*`)
)

// verifyRecordedSchema refuses a database whose schema differs from the one
// the migrations up to its recorded version produce.
//
// Before 1.0 a schema change edits a create migration instead of adding a new
// one, so the version number and the table names can both stay the same while a
// column, default, constraint, or index changes. The expected schema is built
// in memory from the embedded migrations, and the two are compared in the
// normalized form `testdata/sqlite_schema.golden` pins.
func verifyRecordedSchema(
	ctx context.Context,
	sqlDB *sql.DB,
	version int,
) error {
	expected, err := expectedSchemaSnapshot(ctx, version)
	if err != nil {
		return err
	}

	actual, err := schemaSnapshot(ctx, sqlDB)
	if err != nil {
		return err
	}

	if actual == expected {
		return nil
	}

	return errIncompatibleDatabase(version)
}

// errIncompatibleDatabase is the refusal for a database this build cannot use.
// It names every file to remove, because a leftover -wal file next to a new
// database is replayed into it, and the workflow database beside it would
// keep the removed sessions' workflow instances orphaned.
func errIncompatibleDatabase(version int) error {
	return ctxerrors.Wrapf(
		commerr.ErrInvalidState,
		"sqlite database at schema version %d was created by a different "+
			"Peen version and cannot be upgraded; stop Peen, delete %s, "+
			"%s-wal, %s-shm, %s, %s-wal, and %s-shm from the state "+
			"directory, and start it again to create new databases",
		version,
		sqliteFileName,
		sqliteFileName,
		sqliteFileName,
		workflowSQLiteFileName,
		workflowSQLiteFileName,
		workflowSQLiteFileName,
	)
}

// expectedSchemaSnapshot builds the first version migrations in a private
// in-memory database and returns its normalized schema.
func expectedSchemaSnapshot(ctx context.Context, version int) (string, error) {
	order, err := sqliteTableOrder()
	if err != nil {
		return "", err
	}

	if version < 0 || version > len(order) {
		return "", errIncompatibleDatabase(version)
	}

	memory, err := sql.Open("sqlite", memoryDatabaseDSN)
	if err != nil {
		return "", ctxerrors.Wrap(err, "open in-memory sqlite")
	}

	defer func() {
		if closeErr := memory.Close(); closeErr != nil {
			runtimecontext.Logger(ctx).Warn(
				"close in-memory sqlite",
				"err", closeErr,
			)
		}
	}()

	// One connection keeps every statement on the same in-memory database.
	memory.SetMaxOpenConns(1)

	for index, table := range order[:version] {
		statement, readErr := createMigrationSQL(index, table)
		if readErr != nil {
			return "", readErr
		}

		if _, err := memory.ExecContext(ctx, statement); err != nil {
			return "", ctxerrors.Wrapf(
				err,
				"apply the create migration for %s in memory",
				table,
			)
		}
	}

	return schemaSnapshot(ctx, memory)
}

// createMigrationSQL reads the embedded up migration that creates a table.
// index is the table's position in sqliteTableOrder.
func createMigrationSQL(index int, table string) (string, error) {
	name := fmt.Sprintf("%07d_create_%s_table.up.sql", index+1, table)

	content, err := migrations.SQLiteFS.ReadFile(
		sqliteMigrationsPath + "/" + name,
	)
	if err != nil {
		return "", ctxerrors.Wrapf(err, "read sqlite migration %q", name)
	}

	return string(content), nil
}

// schemaSnapshot renders the normalized schema of every table the migrations
// create: columns, defaults, CHECK clauses, foreign keys, unique and primary
// key constraints, and explicit indexes. It leaves out the objects SQLite and
// the migration runner own.
func schemaSnapshot(ctx context.Context, sqlDB *sql.DB) (string, error) {
	tables, err := queryStrings(
		ctx,
		sqlDB,
		`SELECT name FROM sqlite_master
		 WHERE type = 'table'
		   AND name NOT LIKE 'sqlite_%'
		   AND name <> 'schema_migrations'
		 ORDER BY name`,
	)
	if err != nil {
		return "", err
	}

	var out strings.Builder

	for _, table := range tables {
		section, sectionErr := tableSnapshot(ctx, sqlDB, table)
		if sectionErr != nil {
			return "", sectionErr
		}

		out.WriteString(section)
	}

	return out.String(), nil
}

// tableSnapshot renders one table's section of the schema.
func tableSnapshot(
	ctx context.Context,
	sqlDB *sql.DB,
	table string,
) (string, error) {
	createSQL, err := queryStrings(
		ctx,
		sqlDB,
		"SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?",
		table,
	)
	if err != nil {
		return "", err
	}

	if len(createSQL) != 1 {
		return "", ctxerrors.Wrapf(
			commerr.ErrInvalidState,
			"sqlite table %q has no single definition",
			table,
		)
	}

	columns, err := columnLines(ctx, sqlDB, table)
	if err != nil {
		return "", err
	}

	foreignKeys, err := foreignKeyLines(ctx, sqlDB, table)
	if err != nil {
		return "", err
	}

	indexes, err := indexLines(ctx, sqlDB, table)
	if err != nil {
		return "", err
	}

	var out strings.Builder

	fmt.Fprintf(&out, "table %s\n", table)

	for _, line := range columns {
		fmt.Fprintf(&out, "  column %s\n", line)
	}

	for _, clause := range checkClauses(createSQL[0]) {
		fmt.Fprintf(&out, "  %s\n", clause)
	}

	for _, line := range foreignKeys {
		fmt.Fprintf(&out, "  foreign key %s\n", line)
	}

	for _, line := range indexes {
		fmt.Fprintf(&out, "  %s\n", line)
	}

	return out.String(), nil
}

func columnLines(
	ctx context.Context,
	sqlDB *sql.DB,
	table string,
) ([]string, error) {
	return queryStrings(
		ctx,
		sqlDB,
		`SELECT cid || ' ' || name || ' ' || type
		        || ' notnull=' || "notnull"
		        || ' default=' || COALESCE(dflt_value, 'NULL')
		        || ' pk=' || pk
		 FROM pragma_table_info(?) ORDER BY cid`,
		table,
	)
}

func foreignKeyLines(
	ctx context.Context,
	sqlDB *sql.DB,
	table string,
) ([]string, error) {
	lines, err := queryStrings(
		ctx,
		sqlDB,
		`SELECT '(' || group_concat("from", ',') || ') -> '
		        || "table" || '(' || group_concat(COALESCE("to", ''), ',')
		        || ') update=' || on_update || ' delete=' || on_delete
		 FROM (SELECT * FROM pragma_foreign_key_list(?) ORDER BY id, seq)
		 GROUP BY id`,
		table,
	)
	if err != nil {
		return nil, err
	}

	sort.Strings(lines)

	return lines, nil
}

func indexLines(
	ctx context.Context,
	sqlDB *sql.DB,
	table string,
) ([]string, error) {
	entries, err := queryStrings(
		ctx,
		sqlDB,
		`SELECT name || '|' || origin FROM pragma_index_list(?)`,
		table,
	)
	if err != nil {
		return nil, err
	}

	lines := make([]string, 0, len(entries))

	for _, entry := range entries {
		name, origin, _ := strings.Cut(entry, "|")

		line, lineErr := indexLine(ctx, sqlDB, name, origin)
		if lineErr != nil {
			return nil, lineErr
		}

		lines = append(lines, line)
	}

	sort.Strings(lines)

	return lines, nil
}

// indexLine renders an explicit index by its definition and a constraint's
// implicit index by its origin and columns.
func indexLine(
	ctx context.Context,
	sqlDB *sql.DB,
	name, origin string,
) (string, error) {
	if origin == indexOriginCreated {
		definition, err := queryStrings(
			ctx,
			sqlDB,
			"SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?",
			name,
		)
		if err != nil {
			return "", err
		}

		if len(definition) != 1 {
			return "", ctxerrors.Wrapf(
				commerr.ErrInvalidState,
				"sqlite index %q has no single definition",
				name,
			)
		}

		return "index " + normalizeSQL(definition[0]), nil
	}

	columns, err := queryStrings(
		ctx,
		sqlDB,
		`SELECT name FROM pragma_index_info(?) ORDER BY seqno`,
		name,
	)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf(
		"constraint origin=%s (%s)",
		origin,
		strings.Join(columns, ","),
	), nil
}

func normalizeSQL(text string) string {
	text = whitespacePattern.ReplaceAllString(strings.TrimSpace(text), " ")
	text = openSpacePattern.ReplaceAllString(text, "(")
	text = closeSpacePattern.ReplaceAllString(text, ")")

	return commaSpacePattern.ReplaceAllString(text, ",")
}

// checkClauses returns every CHECK (...) clause in a CREATE TABLE statement,
// normalized and sorted.
func checkClauses(createSQL string) []string {
	upper := strings.ToUpper(createSQL)
	clauses := []string{}
	offset := 0

	for {
		index := strings.Index(upper[offset:], checkKeyword)
		if index < 0 {
			break
		}

		start := offset + index
		open := strings.Index(createSQL[start:], "(")
		depth := 0
		end := start + open

		for ; end < len(createSQL); end++ {
			switch createSQL[end] {
			case '(':
				depth++
			case ')':
				depth--
			}

			if depth == 0 {
				break
			}
		}

		clauses = append(clauses, normalizeSQL(createSQL[start:end+1]))
		offset = end + 1
	}

	sort.Strings(clauses)

	return clauses
}

// queryStrings runs a query with bound parameters and returns its single text
// column.
func queryStrings(
	ctx context.Context,
	sqlDB *sql.DB,
	query string,
	args ...any,
) ([]string, error) {
	rows, err := sqlDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "query sqlite schema")
	}

	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			runtimecontext.Logger(ctx).Warn(
				"close sqlite schema rows",
				"err", closeErr,
			)
		}
	}()

	values := []string{}

	for rows.Next() {
		value := ""
		if err := rows.Scan(&value); err != nil {
			return nil, ctxerrors.Wrap(err, "scan sqlite schema value")
		}

		values = append(values, value)
	}

	if err := rows.Err(); err != nil {
		return nil, ctxerrors.Wrap(err, "iterate sqlite schema rows")
	}

	return values, nil
}
