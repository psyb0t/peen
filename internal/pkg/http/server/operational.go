package server

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/psyb0t/ctxerrors"
)

const (
	// healthzPath and readyPath are deliberately unversioned. They belong to
	// the deployment, not to the agent API, so they do not move when /v1 does.
	healthzPath = "/healthz"
	readyPath   = "/ready"

	operationalStatusOK = "ok"
)

// operationalStatus is the probe response body.
type operationalStatus struct {
	Status string `json:"status"`
}

// registerOperationalRoutes mounts the probe endpoints outside the generated
// router.
func (s *Server) registerOperationalRoutes() {
	s.echo.GET(healthzPath, handleOperationalProbe)
	s.echo.GET(readyPath, handleOperationalProbe)
}

// handleOperationalProbe answers both probes.
//
// Readiness and liveness are the same answer here, and that is accurate rather
// than lazy: the database is opened, migrated, and integrity-checked before
// the listener is ever created, so a process that has failed any of those has
// no listener to probe. Reaching this handler means startup completed.
func handleOperationalProbe(c echo.Context) error {
	if err := c.JSON(
		http.StatusOK,
		operationalStatus{Status: operationalStatusOK},
	); err != nil {
		return ctxerrors.Wrap(err, "write probe response")
	}

	return nil
}

// isOperationalPath reports whether a request targets a probe endpoint.
func isOperationalPath(path string) bool {
	trimmed := strings.TrimSuffix(path, "/")

	return trimmed == healthzPath || trimmed == readyPath
}

// skipOperational wraps a middleware so probe requests bypass it.
//
// A probe carries no bearer token, sets no Accept header worth negotiating,
// and is not described by the OpenAPI document, so authentication, response
// negotiation, and spec validation would each reject it on their own terms. An
// orchestrator that cannot health-check a service without credentials will
// simply mark it down.
func skipOperational(middleware echo.MiddlewareFunc) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		wrapped := middleware(next)

		return func(c echo.Context) error {
			if isOperationalPath(c.Request().URL.Path) {
				return next(c)
			}

			return wrapped(c)
		}
	}
}
