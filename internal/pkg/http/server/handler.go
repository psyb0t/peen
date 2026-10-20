package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/aichteeteapee/serbewr/middleware"
	"github.com/psyb0t/ctxerrors"
)

// newInProcessHandler applies the production middleware lists to a plain mux.
// Serbewr owns the network listener and only serves its router there, so this
// handler is what ServeListener serves and what handler tests call directly,
// with the same route handlers and middleware either way.
func newInProcessHandler(
	server *Server,
	apiHandler http.Handler,
	validator middleware.Middleware,
	spaHandler http.Handler,
) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(http.MethodGet+" "+healthzPath, handleOperationalProbe)
	mux.HandleFunc(http.MethodGet+" "+readyPath, handleOperationalProbe)
	mux.Handle(
		http.MethodGet+" "+webSocketPath,
		middleware.Chain(
			http.HandlerFunc(server.handleWebSocket),
			server.authenticate,
		),
	)
	mux.Handle(
		apiBaseURL+"/",
		middleware.Chain(apiHandler, apiMiddlewares(server, validator)...),
	)
	mux.Handle("/", spaHandler)

	return middleware.Chain(mux, globalMiddlewares(server)...)
}

// inProcessUpgradeGate drains upgrade handlers before the hub closes. The
// hub registers a client's connections after it publishes the client, so a
// concurrent close could otherwise stop a client still being initialized.
type inProcessUpgradeGate struct {
	handler http.Handler
	mutex   sync.RWMutex
	closing bool
}

func (g *inProcessUpgradeGate) ServeHTTP(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if request.URL.Path != webSocketPath {
		g.handler.ServeHTTP(writer, request)

		return
	}

	g.mutex.RLock()
	defer g.mutex.RUnlock()

	if g.closing {
		http.Error(
			writer,
			http.StatusText(http.StatusServiceUnavailable),
			http.StatusServiceUnavailable,
		)

		return
	}

	g.handler.ServeHTTP(writer, request)
}

func (g *inProcessUpgradeGate) stopUpgrades() {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	g.closing = true
}

// ServeListener serves the control API on listener until ctx ends or the
// listener fails, instead of binding the configured address.
//
// A controller that must not be reachable from the network passes a listener
// with no network address, such as an in-memory one. The HTTP timeouts match
// the ones Serve uses.
func (s *Server) ServeListener(
	ctx context.Context,
	listener net.Listener,
) error {
	upgradeGate := &inProcessUpgradeGate{handler: s.inProcessHandler}

	closeWebSockets := func() {
		upgradeGate.stopUpgrades()
		s.webSocketHub.Close()
	}
	defer closeWebSockets()

	httpServer := &http.Server{
		Handler:           upgradeGate,
		ReadTimeout:       aichteeteapee.DefaultHTTPServerReadTimeout,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		WriteTimeout:      aichteeteapee.DefaultHTTPServerWriteTimeout,
		IdleTimeout:       aichteeteapee.DefaultHTTPServerIdleTimeout,
		MaxHeaderBytes:    aichteeteapee.DefaultHTTPServerMaxHeaderBytes,
		BaseContext: func(net.Listener) context.Context {
			return context.WithoutCancel(ctx)
		},
	}

	served := make(chan error, 1)

	go func() {
		served <- httpServer.Serve(listener)
	}()

	select {
	case err := <-served:
		return serveListenerResult(err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		aichteeteapee.DefaultHTTPServerShutdownTimeout,
	)
	defer cancel()

	// Shutdown does not wait for hijacked connections, so closing the hub
	// first is what ends the WebSocket clients.
	closeWebSockets()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return ctxerrors.Wrap(err, "shut down the in-process HTTP server")
	}

	return serveListenerResult(<-served)
}

// serveListenerResult treats the error a shutdown makes Serve return as a
// clean stop.
func serveListenerResult(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}

	return ctxerrors.Wrap(err, "serve HTTP requests in process")
}
