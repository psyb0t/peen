package peen

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/auditlog"
	"github.com/psyb0t/peen/internal/pkg/metrics"
)

// AuditLogOptions enables a private daily JSON trace alongside Logger.
// Empty Directory uses StateDirectory/logs. Retention defaults to 14 days.
type AuditLogOptions struct {
	Enabled   bool
	Directory string
	Retention time.Duration
}

type telemetry struct {
	logger     *slog.Logger
	audit      *auditlog.DailyHandler
	metrics    *metrics.Metrics
	registerer prometheus.Registerer
}

func newTelemetry(options Options, stateDirectory string) (*telemetry, error) {
	result := &telemetry{logger: options.Logger}
	if options.AuditLog.Enabled {
		handler, err := auditlog.NewDailyHandler(auditlog.Config{
			StateDirectory: stateDirectory,
			Directory:      options.AuditLog.Directory,
			Retention:      options.AuditLog.Retention,
		})
		if err != nil {
			return nil, ctxerrors.Wrap(err, "create embedding audit log")
		}

		result.audit = handler

		logger := result.logger
		if logger == nil {
			logger = slog.Default()
		}

		result.logger = slog.New(fanoutHandler{logger.Handler(), handler})
	}

	if options.MetricsRegisterer != nil {
		result.metrics = metrics.New()
		if err := options.MetricsRegisterer.Register(
			result.metrics,
		); err != nil {
			return nil, errors.Join(ctxerrors.Wrap(
				err,
				"register embedding metrics",
			), result.close())
		}

		result.registerer = options.MetricsRegisterer
	}

	return result, nil
}

func (t *telemetry) close() error {
	if t == nil {
		return nil
	}

	if t.registerer != nil {
		t.registerer.Unregister(t.metrics)
		t.registerer = nil
	}

	if t.audit != nil {
		if err := t.audit.Close(); err != nil {
			return ctxerrors.Wrap(err, "close embedding audit sink")
		}
	}

	return nil
}

type fanoutHandler []slog.Handler

func (h fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h {
		if handler.Enabled(ctx, level) {
			return true
		}
	}

	return false
}

func (h fanoutHandler) Handle(ctx context.Context, record slog.Record) error {
	var result error

	for _, handler := range h {
		if handler.Enabled(ctx, record.Level) {
			result = errors.Join(result, handler.Handle(ctx, record.Clone()))
		}
	}

	return result
}

func (h fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	result := make(fanoutHandler, 0, len(h))
	for _, handler := range h {
		result = append(result, handler.WithAttrs(attrs))
	}

	return result
}

func (h fanoutHandler) WithGroup(name string) slog.Handler {
	result := make(fanoutHandler, 0, len(h))
	for _, handler := range h {
		result = append(result, handler.WithGroup(name))
	}

	return result
}
