package server

import (
	"net/http"

	"github.com/psyb0t/aichteeteapee/serbewr/middleware"
)

// newTestHandler applies the production middleware lists to an in-memory
// mux. Serbewr owns the production listener; this avoids binding a port for
// every focused handler test while exercising the same route handlers.
func newTestHandler(
	server *Server,
	apiHandler http.Handler,
	validator middleware.Middleware,
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

	return middleware.Chain(mux, globalMiddlewares(server)...)
}
