package server

import (
	"net/http"
	"strings"

	"github.com/psyb0t/aichteeteapee"
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

// handleOperationalProbe answers both probes.
//
// Readiness and liveness are the same answer here, and that is accurate rather
// than lazy: the database is opened, migrated, and integrity-checked before
// the listener is ever created, so a process that has failed any of those has
// no listener to probe. Reaching this handler means startup completed.
func handleOperationalProbe(w http.ResponseWriter, _ *http.Request) {
	aichteeteapee.WriteJSON(
		w,
		http.StatusOK,
		operationalStatus{Status: operationalStatusOK},
	)
}

// isOperationalPath reports whether a request targets a probe endpoint.
func isOperationalPath(path string) bool {
	trimmed := strings.TrimSuffix(path, "/")

	return trimmed == healthzPath || trimmed == readyPath
}
