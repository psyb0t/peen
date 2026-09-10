package server

import (
	"net/http"

	"github.com/psyb0t/aichteeteapee/serbewr"
	"github.com/psyb0t/aichteeteapee/serbewr/middleware"
)

func newRouter(
	server *Server,
	apiHandler http.Handler,
	validator middleware.Middleware,
) *serbewr.Router {
	return &serbewr.Router{
		GlobalMiddlewares: globalMiddlewares(server),
		Groups: []serbewr.GroupConfig{
			{
				Routes: []serbewr.RouteConfig{
					{
						Method:  http.MethodGet,
						Path:    healthzPath,
						Handler: handleOperationalProbe,
					},
					{
						Method:  http.MethodGet,
						Path:    readyPath,
						Handler: handleOperationalProbe,
					},
				},
			},
			{
				Middlewares: []middleware.Middleware{server.authenticate},
				Routes: []serbewr.RouteConfig{
					{
						Method:  http.MethodGet,
						Path:    webSocketPath,
						Handler: server.handleWebSocket,
					},
				},
			},
			{
				Middlewares: apiMiddlewares(server, validator),
				Routes: []serbewr.RouteConfig{
					{Path: apiMountPattern, Handler: apiHandler.ServeHTTP},
				},
			},
		},
	}
}

func globalMiddlewares(server *Server) []middleware.Middleware {
	return []middleware.Middleware{
		normalizeRequestID,
		middleware.RequestID(),
		middleware.Logger(middleware.WithIncludeQuery(false)),
		middleware.Recovery(),
		middleware.SecurityHeaders(),
		middleware.CORS(),
		server.requestMetrics,
	}
}

func apiMiddlewares(
	server *Server,
	validator middleware.Middleware,
) []middleware.Middleware {
	return []middleware.Middleware{
		server.authenticate,
		negotiateResponse,
		limitBody,
		validateJSONBody,
		validator,
	}
}
