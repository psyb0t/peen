// Package server exposes Peen's client-facing Serbewr HTTP API.
package server

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/google/uuid"
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
	Runtime agent.API

	// Sessions opens and lists workspace sessions. It is the control surface's
	// registry rather than the runtime, because opening a workspace is a
	// control operation bounded by deployment policy, not part of running a
	// turn.
	Sessions SessionRegistry

	// Turns dispatches an accepted client message to the session's worker. The
	// control plane routes work; it does not run the model loop.
	Turns TurnRouter

	APIToken       string
	ListenAddress  string
	Metrics        *metrics.Metrics
	ServiceContext func() context.Context
}

// TurnRouter sends one accepted client message to its session's worker.
//
// It is an interface so the HTTP layer depends on dispatching a turn rather
// than on how a worker is found or launched.
type TurnRouter interface {
	RunSessionMessage(
		ctx context.Context,
		sessionID uuid.UUID,
		request agent.MessageRequest,
		requestID uuid.UUID,
	) (*agent.MessageRunResult, error)

	// CancelSessionTurn reaches the worker process running the turn. The
	// controller records the request durably but cannot interrupt a model loop
	// that runs in another process, so cancellation has to travel the same
	// route the turn did.
	CancelSessionTurn(
		ctx context.Context,
		sessionID uuid.UUID,
	) (bool, error)

	// SignalSessionJob reaches the worker whose process group holds the job. A
	// nil response means no live worker holds it, which the caller records
	// against the durable row instead.
	SignalSessionJob(
		ctx context.Context,
		sessionID uuid.UUID,
		jobID uuid.UUID,
		signal string,
	) (*api.JobSignalResponse, error)
}

// SessionRegistry is the control-surface operation set the session endpoints
// need. It is an interface so the HTTP layer depends on the operations rather
// than the registry's construction.
type SessionRegistry interface {
	OpenWorkspaceSession(
		ctx context.Context,
		workspace string,
		profile string,
	) (api.OpenedSession, error)
	ListSessions(
		ctx context.Context,
		params api.ListSessionsParams,
	) (api.SessionPage, error)
	WorkspaceRoots() api.WorkspaceRootList
	ExecutionProfiles() api.ExecutionProfileList
	ListWorkerGenerations(
		ctx context.Context,
		sessionID uuid.UUID,
		params api.ListSessionWorkersParams,
	) (api.WorkerGenerationPage, error)
	ReconfigureSession(
		ctx context.Context,
		sessionID uuid.UUID,
		request api.ReconfigureSessionRequest,
	) (api.SessionProfileDecision, error)
	ListProfileDecisions(
		ctx context.Context,
		sessionID uuid.UUID,
		params api.ListSessionProfileDecisionsParams,
	) (api.SessionProfileDecisionPage, error)
}

// Server owns Peen's Serbewr listener and its generated OpenAPI handler.
type Server struct {
	deps                    Dependencies
	router                  *serbewr.Router
	httpServer              *serbewr.Server
	testHandler             http.Handler
	webSocketHub            wshub.Hub
	webSocketUpgradeHandler http.Handler
	webSocketFilterMutex    sync.Mutex
	webSocketFilters        map[uuid.UUID]webSocketSessionFilterEntry
}

var _ api.StrictServerInterface = (*Server)(nil)

// New constructs a fully-wired HTTP server without opening a listener.
func New(deps Dependencies) (*Server, error) {
	if err := prepareDependencies(&deps); err != nil {
		return nil, err
	}

	instance := &Server{deps: deps}
	instance.configureWebSocketHub()

	if err := instance.configureHandlers(); err != nil {
		instance.webSocketHub.Close()

		return nil, err
	}

	if err := instance.configureHTTPServer(); err != nil {
		instance.webSocketHub.Close()

		return nil, err
	}

	return instance, nil
}

func prepareDependencies(deps *Dependencies) error {
	if deps.Runtime == nil {
		return ctxerrors.Wrap(ErrMissingDependency, "agent runtime")
	}

	if deps.Sessions == nil {
		return ctxerrors.Wrap(ErrMissingDependency, "session registry")
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

	return nil
}

func (s *Server) configureHandlers() error {
	validator, err := specValidator()
	if err != nil {
		return ctxerrors.Wrap(err, "create OpenAPI specification validator")
	}

	apiHandler := s.newAPIHandler()

	spaHandler, err := newSPAHandler()
	if err != nil {
		return ctxerrors.Wrap(err, "create static control-surface handler")
	}

	s.router = newRouter(s, apiHandler, validator, spaHandler)
	s.testHandler = newTestHandler(
		s,
		apiHandler,
		validator,
		spaHandler,
	)

	return nil
}

func (s *Server) configureHTTPServer() error {
	httpServer, err := serbewr.NewWithConfig(
		serbewr.Config{
			ListenAddress:       s.deps.ListenAddress,
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
		return ctxerrors.Wrap(err, "create Serbewr HTTP server")
	}

	s.httpServer = httpServer

	return nil
}

func (s *Server) configureWebSocketHub() {
	s.webSocketHub = wshub.NewHub(webSocketHubName)
	s.webSocketFilters = make(map[uuid.UUID]webSocketSessionFilterEntry)
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
