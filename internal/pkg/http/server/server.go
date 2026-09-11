// Package server exposes Peen's client-facing Serbewr HTTP API.
package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/aichteeteapee/serbewr"
	"github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es/wshub"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/agent"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/metrics"
)

// Dependencies are the transport-neutral operations required by the API.
type Dependencies struct {
	Runtime        agent.API
	APIToken       string
	ListenAddress  string
	Metrics        *metrics.Metrics
	ServiceContext func() context.Context
}

// Server owns Peen's Serbewr listener and its generated OpenAPI handler.
type Server struct {
	deps                    Dependencies
	router                  *serbewr.Router
	httpServer              *serbewr.Server
	testHandler             http.Handler
	webSocketHub            wshub.Hub
	webSocketUpgradeHandler http.Handler
}

var _ api.StrictServerInterface = (*Server)(nil)

// New constructs a fully-wired HTTP server without opening a listener.
func New(deps Dependencies) (*Server, error) {
	if deps.Runtime == nil {
		return nil, ctxerrors.Wrap(ErrMissingDependency, "agent runtime")
	}

	if deps.ListenAddress == "" {
		deps.ListenAddress = aichteeteapee.DefaultHTTPServerListenAddress
	}

	if deps.Metrics == nil {
		deps.Metrics = metrics.New()
	}

	if deps.ServiceContext == nil {
		deps.ServiceContext = context.Background
	}

	instance := &Server{deps: deps}
	instance.configureWebSocketHub()

	validator, err := specValidator()
	if err != nil {
		instance.webSocketHub.Close()

		return nil, err
	}

	apiHandler := instance.newAPIHandler()

	instance.router = newRouter(instance, apiHandler, validator)
	instance.testHandler = newTestHandler(instance, apiHandler, validator)

	instance.httpServer, err = serbewr.NewWithConfig(
		serbewr.Config{
			ListenAddress:       deps.ListenAddress,
			ReadTimeout:         aichteeteapee.DefaultHTTPServerReadTimeout,
			ReadHeaderTimeout:   defaultReadHeaderTimeout,
			WriteTimeout:        aichteeteapee.DefaultHTTPServerWriteTimeout,
			IdleTimeout:         aichteeteapee.DefaultHTTPServerIdleTimeout,
			MaxHeaderBytes:      aichteeteapee.DefaultHTTPServerMaxHeaderBytes,
			ShutdownTimeout:     aichteeteapee.DefaultHTTPServerShutdownTimeout,
			ServiceName:         serviceName,
			FileUploadMaxMemory: aichteeteapee.DefaultFileUploadMaxMemory,
		},
	)
	if err != nil {
		instance.webSocketHub.Close()

		return nil, ctxerrors.Wrap(err, "create Serbewr HTTP server")
	}

	return instance, nil
}

func (s *Server) configureWebSocketHub() {
	s.webSocketHub = wshub.NewHub(webSocketHubName)
	s.webSocketHub.RegisterEventHandler(
		webSocketMessageSendEventType,
		s.handleWebSocketMessage,
	)
	s.webSocketUpgradeHandler = wshub.UpgradeHandler(
		s.webSocketHub,
		wshub.WithUpgradeHandlerSubprotocols(webSocketSubprotocol),
	)
}

func (s *Server) newAPIHandler() http.Handler {
	strictHandler := api.NewStrictHandlerWithOptions(
		s,
		nil,
		api.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  s.handleRequestError,
			ResponseErrorHandlerFunc: s.handleResponseError,
		},
	)

	return api.HandlerWithOptions(
		strictHandler,
		api.StdHTTPServerOptions{
			BaseURL:          apiBaseURL,
			ErrorHandlerFunc: s.handleRequestError,
		},
	)
}

//nolint:lll // The upstream constant name is part of the direct Serbewr configuration.
const defaultReadHeaderTimeout = aichteeteapee.DefaultHTTPServerReadHeaderTimeout

// Serve listens until the service context is cancelled or the listener fails.
func (s *Server) Serve(ctx context.Context) error {
	defer s.webSocketHub.Close()

	if err := s.httpServer.Start(ctx, s.router); err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return nil
		}

		return ctxerrors.Wrap(err, "serve HTTP requests")
	}

	return nil
}

// Stop shuts down Serbewr. It is safe to call after Serve has already
// stopped because Serbewr's shutdown is idempotent.
func (s *Server) Stop(ctx context.Context) error {
	s.webSocketHub.Close()

	if err := s.httpServer.Stop(ctx); err != nil {
		return ctxerrors.Wrap(err, "stop HTTP server")
	}

	return nil
}
