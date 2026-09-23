package server

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
)

//go:embed all:web/dist
var webDistFS embed.FS

// newSPAHandler serves the embedded static client. API and WebSocket paths
// are registered before this catch-all route. Unknown browser paths return the
// shell so the client can resolve its own route.
func newSPAHandler() (http.Handler, error) {
	staticFS, err := fs.Sub(webDistFS, webDistRoot)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "sub embedded static client filesystem")
	}

	fileServer := http.FileServer(http.FS(staticFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set(aichteeteapee.HeaderNameAllow, spaAllowedMethods)
			w.WriteHeader(http.StatusMethodNotAllowed)

			return
		}

		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = spaIndexFile
		}

		if _, statErr := fs.Stat(staticFS, name); statErr != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/" + spaIndexFile
		}

		fileServer.ServeHTTP(w, r)
	}), nil
}
