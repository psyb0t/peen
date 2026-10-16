package server

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
)

//go:embed all:web/dist
var webDistFS embed.FS

// newSPAHandler serves the embedded static client. API and WebSocket paths
// are registered before this catch-all route. Unknown browser paths return the
// shell so the client can resolve its own route, except a path still shaped
// like the previous /v1 split (see isStaleAPIPath), which answers a JSON 404
// instead so a client built against the old paths fails loudly.
func newSPAHandler() (http.Handler, error) {
	staticFS, err := fs.Sub(webDistFS, webDistRoot)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "sub embedded static client filesystem")
	}

	fileServer := http.FileServer(http.FS(staticFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Checked ahead of the method guard below: a stale prefix is never a
		// real SPA asset path, regardless of method, so it always answers
		// the same JSON 404 rather than a method-not-allowed meant for the
		// static file server.
		if isStaleAPIPath(r.URL.Path) {
			writeAPIError(
				w,
				http.StatusNotFound,
				aichteeteapee.ErrorCodeNotFound,
				staleAPIPathMessage,
			)

			return
		}

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set(aichteeteapee.HeaderNameAllow, spaAllowedMethods)
			w.WriteHeader(http.StatusMethodNotAllowed)

			return
		}

		if isPrivateMetricsPath(r.URL.Path) {
			http.NotFound(w, r)

			return
		}

		name := spaAssetName(r.URL.Path)
		if _, statErr := fs.Stat(staticFS, name); statErr != nil {
			name = spaIndexFile
		}

		r = r.Clone(r.Context())
		r.URL.Path = spaFileServerPath(name)
		r.URL.RawPath = ""

		fileServer.ServeHTTP(w, r)
	}), nil
}

func spaAssetName(requestPath string) string {
	name := strings.TrimPrefix(requestPath, "/")
	if name == "" || name == "." || !fs.ValidPath(name) {
		return spaIndexFile
	}

	return name
}

func spaFileServerPath(name string) string {
	if name == spaIndexFile {
		return "/"
	}

	return "/" + name
}

func isPrivateMetricsPath(requestPath string) bool {
	return requestPath == privateMetricsPath || strings.HasPrefix(
		requestPath,
		privateMetricsPath+"/",
	)
}

// isStaleAPIPath reports whether a request targets a bare or prefixed
// /v1, /api, or /ws path that no live route matched. /api/v1/... and
// /ws/v1 requests are handled by their own route groups before the SPA
// catch-all ever sees them, so this only catches a client still built
// against the previous unversioned /v1 split.
func isStaleAPIPath(requestPath string) bool {
	for _, prefix := range []string{
		staleAPIPathV1,
		staleAPIPathAPI,
		staleAPIPathWS,
	} {
		if requestPath == prefix ||
			strings.HasPrefix(requestPath, prefix+"/") {
			return true
		}
	}

	return false
}
