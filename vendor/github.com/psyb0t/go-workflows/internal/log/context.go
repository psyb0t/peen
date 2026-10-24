package log

import (
	"context"
	"log/slog"

	"github.com/psyb0t/ctxscope"
)

// ScopeLogger preserves the caller's sink and lets Context calls carry scope.
func ScopeLogger(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		logger = slog.Default()
	}
	if _, ok := logger.Handler().(*ctxscope.Handler); ok {
		return logger
	}
	return slog.New(ctxscope.NewHandler(logger.Handler()))
}

// WithContext binds ordinary Logger calls to an activity or replay-aware workflow context.
func WithContext(logger *slog.Logger, ctx context.Context) *slog.Logger {
	return slog.New(&contextHandler{inner: logger.Handler(), ctx: ctx})
}

type contextHandler struct {
	inner slog.Handler
	ctx   context.Context
}

func (h *contextHandler) Enabled(_ context.Context, level slog.Level) bool {
	return h.inner.Enabled(h.ctx, level)
}

func (h *contextHandler) Handle(_ context.Context, record slog.Record) error {
	return h.inner.Handle(h.ctx, record)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{inner: h.inner.WithAttrs(attrs), ctx: h.ctx}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{inner: h.inner.WithGroup(name), ctx: h.ctx}
}
