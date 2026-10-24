package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/psyb0t/ctxscope"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	gormMessageLog   = "gorm message"
	gormErrorLog     = "gorm error"
	gormSlowQueryLog = "SLOW QUERY"
	gormQueryLog     = "SQL EXECUTED"

	gormDetailKey     = "detail"
	gormErrorKey      = "error"
	gormDurationKey   = "duration"
	gormRowsKey       = "rows"
	gormSQLPreviewKey = "sql_preview"
	gormSQLBytesKey   = "sql_bytes"
	gormSQLCutKey     = "sql_truncated"
)

// GormSlogLogger writes GORM's logs through the context's ctxscope logger, so
// every record carries the caller's scope.
type GormSlogLogger struct {
	LogLevel logger.LogLevel
	// IgnoreRecordNotFoundError logs a query that found no record as an
	// ordinary query instead of an error, like GORM's own logger option. A
	// caller that polls for usually absent work otherwise logs an error on
	// every empty poll.
	IgnoreRecordNotFoundError bool
}

// NewGormSlogLogger logs at logger.Info and does not treat
// gorm.ErrRecordNotFound as an error.
func NewGormSlogLogger() logger.Interface { //nolint:ireturn
	return &GormSlogLogger{
		LogLevel:                  logger.Info,
		IgnoreRecordNotFoundError: true,
	}
}

func (l *GormSlogLogger) LogMode(level logger.LogLevel) logger.Interface { //nolint:ireturn
	newLogger := *l
	newLogger.LogLevel = level

	return &newLogger
}

// Info, Warn and Error log GORM's own messages. GORM hands over a format
// string and its values, so the rendered text goes in the detail field under
// a fixed message.
func (l *GormSlogLogger) Info(ctx context.Context, msg string, data ...any) {
	if l.LogLevel >= logger.Info {
		ctxscope.GetLogger(ctx).InfoContext(ctx, gormMessageLog,
			gormDetailKey, fmt.Sprintf(msg, data...))
	}
}

func (l *GormSlogLogger) Warn(ctx context.Context, msg string, data ...any) {
	if l.LogLevel >= logger.Warn {
		ctxscope.GetLogger(ctx).WarnContext(ctx, gormMessageLog,
			gormDetailKey, fmt.Sprintf(msg, data...))
	}
}

func (l *GormSlogLogger) Error(ctx context.Context, msg string, data ...any) {
	if l.LogLevel >= logger.Error {
		ctxscope.GetLogger(ctx).ErrorContext(ctx, gormMessageLog,
			gormDetailKey, fmt.Sprintf(msg, data...))
	}
}

// isLoggedError reports whether Trace logs err as an error. A missing record
// is not one when IgnoreRecordNotFoundError is set.
func (l *GormSlogLogger) isLoggedError(err error) bool {
	if err == nil {
		return false
	}

	return !l.IgnoreRecordNotFoundError ||
		!errors.Is(err, gorm.ErrRecordNotFound)
}

const slowQueryThreshold = 200 * time.Millisecond

const maxSQLPreviewBytes = 2048

type sqlSummary struct {
	Preview   string
	Bytes     int
	Truncated bool
}

func summarizeSQL(query string) sqlSummary {
	preview, truncated := redactSQLValues(query, maxSQLPreviewBytes)

	return sqlSummary{
		Preview:   preview,
		Bytes:     len(query),
		Truncated: truncated,
	}
}

//nolint:cyclop // bounded scanner branches by SQL token class
func redactSQLValues(query string, maxBytes int) (string, bool) {
	var out strings.Builder

	out.Grow(min(len(query), maxBytes))

	lastWasSpace := false

	for i := 0; i < len(query); {
		if isEscapeStringStart(query, i) {
			const escapeStringPrefixBytes = 2

			out.WriteByte('?')

			i = skipEscapeQuotedLiteral(query, i+escapeStringPrefixBytes)
			lastWasSpace = false
		} else if delimiter, ok := dollarQuoteDelimiter(query, i); ok {
			out.WriteByte('?')

			i = skipDollarQuotedLiteral(query, i+len(delimiter), delimiter)
			lastWasSpace = false
		} else {
			r, size := utf8.DecodeRuneInString(query[i:])

			switch {
			case r == '\'':
				out.WriteByte('?')

				i = skipQuotedLiteral(query, i+size)
				lastWasSpace = false
			case unicode.IsSpace(r):
				// Leading whitespace is dropped rather than collapsed, so the
				// preview never opens with a space that TrimSpace would remove.
				if !lastWasSpace && out.Len() > 0 {
					out.WriteByte(' ')
				}

				i += size
				lastWasSpace = true
			case unicode.IsDigit(r):
				out.WriteByte('?')

				i = skipNumericLiteral(query, i+size)
				lastWasSpace = false
			default:
				out.WriteRune(r)

				i += size
				lastWasSpace = false
			}
		}

		if out.Len() >= maxBytes && i < len(query) {
			preview, _ := truncateUTF8(strings.TrimSpace(out.String()), maxBytes)

			return preview, true
		}
	}

	return strings.TrimSpace(out.String()), false
}

func isEscapeStringStart(query string, i int) bool {
	return i+1 < len(query) &&
		(query[i] == 'E' || query[i] == 'e') &&
		query[i+1] == '\''
}

func skipEscapeQuotedLiteral(query string, i int) int {
	for i < len(query) {
		if query[i] == '\\' {
			i++
			if i < len(query) {
				_, size := utf8.DecodeRuneInString(query[i:])
				i += size
			}

			continue
		}

		next, size := utf8.DecodeRuneInString(query[i:])
		i += size

		if next != '\'' {
			continue
		}

		if i < len(query) && query[i] == '\'' {
			i++

			continue
		}

		return i
	}

	return i
}

func dollarQuoteDelimiter(query string, i int) (string, bool) {
	if i >= len(query) || query[i] != '$' {
		return "", false
	}

	relativeEnd := strings.IndexByte(query[i+1:], '$')
	if relativeEnd < 0 {
		return "", false
	}

	end := i + 1 + relativeEnd
	if !validDollarQuoteTag(query[i+1 : end]) {
		return "", false
	}

	return query[i : end+1], true
}

func validDollarQuoteTag(tag string) bool {
	for i, char := range []byte(tag) {
		if !validDollarQuoteTagChar(char, i == 0) {
			return false
		}
	}

	return true
}

func validDollarQuoteTagChar(char byte, first bool) bool {
	if char == '_' {
		return true
	}

	if char >= '0' && char <= '9' {
		return !first
	}

	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
}

func skipDollarQuotedLiteral(query string, i int, delimiter string) int {
	relativeEnd := strings.Index(query[i:], delimiter)
	if relativeEnd < 0 {
		return len(query)
	}

	return i + relativeEnd + len(delimiter)
}

// skipQuotedLiteral returns the index just past the single-quoted literal whose
// opening quote has already been consumed. Two consecutive single quotes are
// SQL's escape for a literal quote, so they continue the string rather than
// ending it — getting that wrong would close the literal early and spill the
// rest of a value into the preview unredacted.
func skipQuotedLiteral(query string, i int) int {
	for i < len(query) {
		next, size := utf8.DecodeRuneInString(query[i:])
		i += size

		if next != '\'' {
			continue
		}

		if i < len(query) && query[i] == '\'' {
			i++

			continue
		}

		return i
	}

	return i
}

// skipNumericLiteral returns the index just past the numeric literal whose
// first digit has already been consumed. A '.' is consumed as part of the
// number so a decimal is redacted whole rather than leaving a trailing
// fragment.
func skipNumericLiteral(query string, i int) int {
	for i < len(query) {
		next, size := utf8.DecodeRuneInString(query[i:])
		if !unicode.IsDigit(next) && next != '.' {
			return i
		}

		i += size
	}

	return i
}

func truncateUTF8(value string, maxBytes int) (string, bool) {
	if len(value) <= maxBytes {
		return value, false
	}

	const truncationMarker = "…"

	end := maxBytes - len(truncationMarker)
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}

	return value[:end] + truncationMarker, true
}

func (l *GormSlogLogger) Trace(
	ctx context.Context,
	begin time.Time,
	fc func() (string, int64),
	err error,
) {
	if l.LogLevel <= logger.Silent {
		return
	}

	elapsed := time.Since(begin)
	isError := l.isLoggedError(err)

	logLevel, message, shouldLog := l.traceSlogLevel(elapsed, isError)
	if !shouldLog {
		return
	}

	scoped := ctxscope.GetLogger(ctx)
	if !scoped.Enabled(ctx, logLevel) {
		return
	}

	sql, rows := fc()
	summary := summarizeSQL(sql)
	attrs := []any{
		gormDurationKey, elapsed,
		gormRowsKey, rows,
		gormSQLPreviewKey, summary.Preview,
		gormSQLBytesKey, summary.Bytes,
		gormSQLCutKey, summary.Truncated,
	}

	if isError {
		attrs = append([]any{gormErrorKey, err}, attrs...)
	}

	scoped.Log(ctx, logLevel, message, attrs...)
}

// traceSlogLevel picks the level and message of one query record, and
// reports false when LogLevel leaves the query unlogged.
func (l *GormSlogLogger) traceSlogLevel(
	elapsed time.Duration,
	isError bool,
) (slog.Level, string, bool) {
	switch {
	case isError && l.LogLevel >= logger.Error:
		return slog.LevelError, gormErrorLog, true
	case elapsed > slowQueryThreshold && l.LogLevel >= logger.Warn:
		return slog.LevelWarn, gormSlowQueryLog, true
	case l.LogLevel >= logger.Info:
		return slog.LevelDebug, gormQueryLog, true
	default:
		return slog.LevelDebug, "", false
	}
}
