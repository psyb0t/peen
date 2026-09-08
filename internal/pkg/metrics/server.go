package metrics

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/common/expfmt"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

const metricsShutdownTimeout = 10 * time.Second

// Server exposes a Metrics registry on an operator-owned listener. It is
// intentionally separate from Peen's public API router.
type Server struct {
	httpServer *http.Server
}

// NewServer constructs a scraper-only HTTP server without opening a listener.
func NewServer(metrics *Metrics) (*Server, error) {
	if metrics == nil || metrics.Registry() == nil {
		return nil, ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet,
			"metrics registry",
		)
	}

	return &Server{httpServer: &http.Server{
		Handler:           metrics.Handler(),
		ReadHeaderTimeout: metricsShutdownTimeout,
	}}, nil
}

// Handler returns the OpenMetrics text-compatible Prometheus scrape handler.
func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Path != "/metrics" {
				http.NotFound(writer, request)

				return
			}

			families, err := m.Registry().Gather()
			if err != nil {
				http.Error(
					writer,
					"metrics unavailable",
					http.StatusInternalServerError,
				)

				return
			}

			writer.Header().Set("Content-Type", string(expfmt.FmtText))

			for _, family := range families {
				if _, err := expfmt.MetricFamilyToText(
					writer,
					family,
				); err != nil {
					return
				}
			}
		},
	)
}

// ServeListener serves the scraper endpoint until the service context ends.
func (s *Server) ServeListener(
	ctx context.Context,
	listener net.Listener,
) error {
	if listener == nil {
		return ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet,
			"metrics listener",
		)
	}

	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- s.httpServer.Serve(listener)
	}()

	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return ctxerrors.Wrap(err, "serve metrics")
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			metricsShutdownTimeout,
		)
		defer cancel()

		if err := s.httpServer.Shutdown(shutdownContext); err != nil {
			return ctxerrors.Wrap(err, "shut down metrics server")
		}

		serveErr := <-serveErrors
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return ctxerrors.Wrap(serveErr, "wait for metrics shutdown")
		}

		return nil
	}
}
