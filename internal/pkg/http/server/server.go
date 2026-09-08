// Package server exposes Peen's client-facing Echo HTTP API.
package server

import (
	"context"
	"errors"
	"net"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/agent"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/metrics"
)

// Dependencies are the transport-neutral operations required by the API.
type Dependencies struct {
	Runtime  agent.API
	APIToken string
	Metrics  *metrics.Metrics
}

// Server owns Peen's Echo router and HTTP lifecycle.
type Server struct {
	deps       Dependencies
	echo       *echo.Echo
	httpServer *http.Server
}

var _ api.StrictServerInterface = (*Server)(nil)

// New constructs a fully-wired HTTP server without opening a listener.
func New(deps Dependencies) (*Server, error) {
	if deps.Runtime == nil {
		return nil, ctxerrors.Wrap(ErrMissingDependency, "agent runtime")
	}

	validator, err := specValidator()
	if err != nil {
		return nil, err
	}

	instance := &Server{deps: deps}
	instance.echo = echo.New()
	instance.echo.HideBanner = true
	instance.echo.HTTPErrorHandler = instance.handleHTTPError
	instance.echo.JSONSerializer = strictJSONSerializer{}
	instance.echo.Use(
		instance.requestContext,
		instance.accessLog,
		instance.recoverPanic,
		// Everything from here down is about the agent API, so the
		// unversioned probes skip it. They still get request scope, access
		// logging, and panic recovery.
		skipOperational(instance.authenticate),
		skipOperational(instance.negotiateResponse),
		skipOperational(instance.limitBody),
		// After limitBody: the validator reads the body, so an oversized one
		// is rejected on size before anything tries to parse it.
		skipOperational(validator),
	)

	instance.registerOperationalRoutes()

	api.RegisterHandlersWithBaseURL(
		instance.echo,
		api.NewStrictHandler(instance, nil),
		apiBaseURL,
	)
	instance.httpServer = &http.Server{
		Handler:           instance.echo,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	return instance, nil
}

// Serve listens until the service context is cancelled or the listener fails.
func (s *Server) Serve(ctx context.Context, address string) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, networkTCP, address)
	if err != nil {
		return ctxerrors.Wrap(err, "listen for HTTP requests")
	}

	return s.ServeListener(ctx, listener)
}

// ServeListener serves a caller-owned listener until its context ends.
func (s *Server) ServeListener(
	ctx context.Context,
	listener net.Listener,
) error {
	if listener == nil {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "HTTP listener")
	}

	ctxscope.GetLogger(ctx).Info(
		"HTTP server listening",
		"address", listener.Addr().String(),
	)

	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- s.httpServer.Serve(listener)
	}()

	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return ctxerrors.Wrap(err, "serve HTTP requests")
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			shutdownTimeout,
		)
		defer cancel()

		if err := s.httpServer.Shutdown(shutdownContext); err != nil {
			return ctxerrors.Wrap(err, "shut down HTTP server")
		}

		serveErr := <-serveErrors
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return ctxerrors.Wrap(serveErr, "wait for HTTP server shutdown")
		}

		ctxscope.GetLogger(ctx).Info("HTTP server stopped")

		return nil
	}
}
