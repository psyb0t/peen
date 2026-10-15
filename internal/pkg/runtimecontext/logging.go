// Package runtimecontext carries local embedding dependencies across calls.
package runtimecontext

import (
	"context"
	"log/slog"
	"maps"
	"slices"

	"github.com/psyb0t/ctxscope"
)

const keyValueAttributeSize = 2

type loggerKey struct{}

// WithLogger preserves runtime isolation without changing the process logger.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	if logger == nil {
		return ctx
	}

	return context.WithValue(ctx, loggerKey{}, logger)
}

// WithLoggerFrom carries an injected logger and keeps destination scope.
func WithLoggerFrom(ctx, source context.Context) context.Context {
	logger, _ := source.Value(loggerKey{}).(*slog.Logger)

	return WithLogger(ctx, logger)
}

// Logger applies the same scope as ctxscope to the embedding caller's sink.
// Controller and worker calls without an injected logger use ctxscope directly.
func Logger(ctx context.Context) *slog.Logger {
	logger, ok := ctx.Value(loggerKey{}).(*slog.Logger)
	if !ok {
		return ctxscope.GetLogger(ctx)
	}

	scope := ctxscope.GetGlobal()
	maps.Copy(scope, ctxscope.Get(ctx))
	keys := slices.Sorted(maps.Keys(scope))

	attributes := make([]any, 0, len(keys)*keyValueAttributeSize)
	for _, key := range keys {
		attributes = append(attributes, key, scope[key])
	}

	return logger.With(attributes...)
}
